package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"novastream/internal/requestsecurity"
	"novastream/models"
	metadatapkg "novastream/services/metadata"
)

var stremioDirectoryManifestPattern = regexp.MustCompile(`"manifestUrl"\s*:\s*"([^"]+)"`)

const (
	stremioShelfCatalogTTL      = 10 * time.Minute
	stremioShelfMaxCatalogItems = 500
	stremioShelfMaxResponseSize = 4 << 20
)

type stremioShelfCatalogCacheEntry struct {
	metas   []stremioMeta
	fetched time.Time
}

type stremioManifestCatalogResponse struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	PageSize int    `json:"pageSize,omitempty"`
}

type stremioManifestIngestionResponse struct {
	ID          string                           `json:"id"`
	Name        string                           `json:"name"`
	Version     string                           `json:"version,omitempty"`
	ManifestURL string                           `json:"manifestUrl"`
	Catalogs    []stremioManifestCatalogResponse `json:"catalogs"`
}

func newStremioShelfHTTPClient() *http.Client {
	return requestsecurity.NewSafeHTTPClient(15*time.Second, 5, nil)
}

func (h *MetadataHandler) stremioShelfClient() *http.Client {
	if h.stremioHTTPClient != nil {
		return h.stremioHTTPClient
	}
	h.stremioHTTPClient = newStremioShelfHTTPClient()
	return h.stremioHTTPClient
}

// normalizeStremioManifestInput accepts a manifest URL, an addon base URL, a
// stremio:// install URL, or Stremio Web's addon URL and returns the canonical
// HTTP(S) manifest URL plus its resource base URL.
func normalizeStremioManifestInput(raw string) (string, string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", "", fmt.Errorf("manifest URL is required")
	}
	if strings.HasPrefix(strings.ToLower(value), "stremio://") {
		value = "https://" + value[len("stremio://"):]
	}
	if parsedWeb, err := url.Parse(value); err == nil && strings.EqualFold(parsedWeb.Hostname(), "web.stremio.com") {
		fragment := strings.TrimPrefix(parsedWeb.Fragment, "/")
		if idx := strings.Index(fragment, "?"); idx >= 0 {
			if addonURL := strings.TrimSpace(parseQueryValue(fragment[idx+1:], "addon")); addonURL != "" {
				value = addonURL
			}
		}
	}

	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return "", "", fmt.Errorf("invalid Stremio manifest URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", fmt.Errorf("Stremio manifest URL must use HTTP or HTTPS")
	}
	if parsed.User != nil {
		return "", "", fmt.Errorf("Stremio manifest URL must not contain embedded credentials")
	}
	parsed.Fragment = ""
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(strings.ToLower(path), "/manifest.json") {
		path += "/manifest.json"
	}
	parsed.Path = path
	manifestURL := parsed.String()

	base := *parsed
	base.Path = strings.TrimSuffix(parsed.Path, "/manifest.json")
	base.RawQuery = ""
	base.ForceQuery = false
	return manifestURL, strings.TrimRight(base.String(), "/"), nil
}

func parseQueryValue(rawQuery, name string) string {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return ""
	}
	return values.Get(name)
}

func stremioAddonDirectoryPageURL(raw string) (string, bool, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return "", false, nil
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "stremio-addons.net" && host != "www.stremio-addons.net" {
		return "", false, nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false, fmt.Errorf("Stremio add-on page URL must use HTTP or HTTPS")
	}
	if parsed.User != nil {
		return "", false, fmt.Errorf("Stremio add-on page URL must not contain embedded credentials")
	}
	if !strings.HasPrefix(strings.TrimRight(parsed.Path, "/"), "/addons/") {
		return "", false, nil
	}
	parsed.Fragment = ""
	return parsed.String(), true, nil
}

func extractStremioDirectoryManifestURL(body []byte) (string, error) {
	// Next.js serializes the page data inside script tags, where JSON quotes can
	// be escaped. Normalize those quotes before locating the manifestUrl field.
	normalized := strings.ReplaceAll(string(body), `\"`, `"`)
	match := stremioDirectoryManifestPattern.FindStringSubmatch(normalized)
	if len(match) != 2 {
		return "", fmt.Errorf("add-on page does not expose a manifest URL")
	}
	value := strings.TrimSpace(match[1])
	value = strings.ReplaceAll(value, `\u0026`, "&")
	if value == "" {
		return "", fmt.Errorf("add-on page exposes an empty manifest URL")
	}
	return value, nil
}

func resolveStremioDirectoryManifestURL(ctx context.Context, client *http.Client, pageURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", liveStreamUserAgent)
	response, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("directory page returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, stremioShelfMaxResponseSize+1))
	if err != nil {
		return "", err
	}
	if len(body) > stremioShelfMaxResponseSize {
		return "", fmt.Errorf("directory page exceeds %d bytes", stremioShelfMaxResponseSize)
	}
	return extractStremioDirectoryManifestURL(body)
}

func (h *MetadataHandler) loadStremioManifest(ctx context.Context, rawURL string) (*stremioManifest, string, string, error) {
	if pageURL, isDirectoryPage, err := stremioAddonDirectoryPageURL(rawURL); err != nil {
		return nil, "", "", err
	} else if isDirectoryPage {
		rawURL, err = resolveStremioDirectoryManifestURL(ctx, h.stremioShelfClient(), pageURL)
		if err != nil {
			return nil, "", "", fmt.Errorf("resolve Stremio add-on page: %w", err)
		}
	}
	manifestURL, baseURL, err := normalizeStremioManifestInput(rawURL)
	if err != nil {
		return nil, "", "", err
	}
	var manifest stremioManifest
	if err := getStremioShelfJSON(ctx, h.stremioShelfClient(), manifestURL, &manifest); err != nil {
		return nil, "", "", fmt.Errorf("fetch Stremio manifest: %w", err)
	}
	if len(manifest.Catalogs) == 0 {
		return nil, "", "", fmt.Errorf("Stremio manifest has no catalogs")
	}
	return &manifest, manifestURL, baseURL, nil
}

// StremioManifest discovers the movie and series catalogs that can be ingested
// as independent home shelves.
func (h *MetadataHandler) StremioManifest(w http.ResponseWriter, r *http.Request) {
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if _, isDirectoryPage, directoryErr := stremioAddonDirectoryPageURL(rawURL); directoryErr != nil {
		writeJSONError(w, directoryErr.Error(), http.StatusBadRequest)
		return
	} else if !isDirectoryPage {
		if _, _, err := normalizeStremioManifestInput(rawURL); err != nil {
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	manifest, manifestURL, _, err := h.loadStremioManifest(r.Context(), rawURL)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadGateway)
		return
	}
	catalogs := make([]stremioManifestCatalogResponse, 0, len(manifest.Catalogs))
	for _, catalog := range manifest.Catalogs {
		mediaType := normalizeStremioCatalogType(catalog.Type)
		if mediaType == "" || strings.TrimSpace(catalog.ID) == "" {
			continue
		}
		name := strings.TrimSpace(catalog.Name)
		if name == "" {
			name = strings.TrimSpace(catalog.ID)
		}
		catalogs = append(catalogs, stremioManifestCatalogResponse{
			Type:     mediaType,
			ID:       strings.TrimSpace(catalog.ID),
			Name:     name,
			PageSize: catalog.PageSize,
		})
	}
	if len(catalogs) == 0 {
		writeJSONError(w, "Stremio manifest has no movie or series catalogs", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(manifest.Name)
	if name == "" {
		name = strings.TrimSpace(manifest.ID)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stremioManifestIngestionResponse{
		ID:          strings.TrimSpace(manifest.ID),
		Name:        name,
		Version:     strings.TrimSpace(manifest.Version),
		ManifestURL: manifestURL,
		Catalogs:    catalogs,
	})
}

// StremioList loads one advertised add-on catalog and feeds its identities into
// the shared curated-list enrichment/filtering pipeline.
func (h *MetadataHandler) StremioList(w http.ResponseWriter, r *http.Request) {
	rawManifestURL := strings.TrimSpace(r.URL.Query().Get("manifestUrl"))
	catalogType := normalizeStremioCatalogType(r.URL.Query().Get("catalogType"))
	catalogID := strings.TrimSpace(r.URL.Query().Get("catalogId"))
	if rawManifestURL == "" || catalogType == "" || catalogID == "" {
		writeJSONError(w, "manifestUrl, catalogType, and catalogId are required", http.StatusBadRequest)
		return
	}
	manifestURL, _, err := normalizeStremioManifestInput(rawManifestURL)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	metas, err := h.loadStremioShelfCatalog(r.Context(), manifestURL, catalogType, catalogID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadGateway)
		return
	}
	curated := make([]metadatapkg.CuratedItem, 0, len(metas))
	for _, meta := range metas {
		item := curatedItemFromStremioMeta(meta, catalogType)
		if item.Title == "" && item.IMDBID == "" && item.TMDBID == 0 {
			continue
		}
		curated = append(curated, item)
	}
	if len(curated) == 0 {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CustomListResponse{Items: []models.TrendingItem{}, Total: 0})
		return
	}

	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	hideUnreleased := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("hideUnreleased")), "true")
	hideWatched := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("hideWatched")), "true")
	limit, offset := parseLimitOffset(r)
	label := strings.TrimSpace(r.URL.Query().Get("name"))
	if label == "" {
		label = catalogID
	}
	response := h.buildShelfFromCurated(w, r, curated, label, userID, hideUnreleased, hideWatched, limit, offset)
	if response == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(response)
}

func (h *MetadataHandler) loadStremioShelfCatalog(ctx context.Context, rawManifestURL, catalogType, catalogID string) ([]stremioMeta, error) {
	manifestURL, _, err := normalizeStremioManifestInput(rawManifestURL)
	if err != nil {
		return nil, err
	}
	cacheKey := manifestURL + "|" + catalogType + "|" + catalogID
	h.stremioCatalogMu.Lock()
	if entry, ok := h.stremioCatalogCache[cacheKey]; ok && time.Since(entry.fetched) < stremioShelfCatalogTTL {
		metas := append([]stremioMeta(nil), entry.metas...)
		h.stremioCatalogMu.Unlock()
		return metas, nil
	}
	h.stremioCatalogMu.Unlock()

	manifest, _, baseURL, err := h.loadStremioManifest(ctx, manifestURL)
	if err != nil {
		return nil, err
	}
	var selected *stremioCatalogDef
	for i := range manifest.Catalogs {
		catalog := &manifest.Catalogs[i]
		if normalizeStremioCatalogType(catalog.Type) == catalogType && strings.TrimSpace(catalog.ID) == catalogID {
			selected = catalog
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("Stremio catalog is not advertised by the manifest")
	}
	metas, err := fetchStremioShelfCatalog(ctx, h.stremioShelfClient(), baseURL, *selected)
	if err != nil {
		return nil, fmt.Errorf("fetch Stremio catalog: %w", err)
	}
	h.stremioCatalogMu.Lock()
	h.stremioCatalogCache[cacheKey] = stremioShelfCatalogCacheEntry{
		metas:   append([]stremioMeta(nil), metas...),
		fetched: time.Now(),
	}
	h.stremioCatalogMu.Unlock()
	return metas, nil
}

func fetchStremioShelfCatalog(ctx context.Context, client *http.Client, baseURL string, catalog stremioCatalogDef) ([]stremioMeta, error) {
	supportsSkip := false
	for _, extra := range catalog.Extra {
		if strings.EqualFold(strings.TrimSpace(extra.Name), "skip") {
			supportsSkip = true
			break
		}
	}
	pageSize := catalog.PageSize
	if pageSize <= 0 || pageSize > stremioShelfMaxCatalogItems {
		pageSize = stremioCatalogPageSize
	}
	var all []stremioMeta
	for page := 0; len(all) < stremioShelfMaxCatalogItems; page++ {
		endpoint := fmt.Sprintf("%s/catalog/%s/%s.json", baseURL, url.PathEscape(catalog.Type), url.PathEscape(catalog.ID))
		if page > 0 {
			endpoint = fmt.Sprintf("%s/catalog/%s/%s/skip=%d.json", baseURL, url.PathEscape(catalog.Type), url.PathEscape(catalog.ID), page*pageSize)
		}
		var response stremioCatalogResponse
		if err := getStremioShelfJSON(ctx, client, endpoint, &response); err != nil {
			if page == 0 {
				return nil, err
			}
			break
		}
		if len(response.Metas) == 0 {
			break
		}
		remaining := stremioShelfMaxCatalogItems - len(all)
		if len(response.Metas) > remaining {
			response.Metas = response.Metas[:remaining]
		}
		all = append(all, response.Metas...)
		if !supportsSkip || len(response.Metas) < pageSize {
			break
		}
	}
	return all, nil
}

func getStremioShelfJSON(ctx context.Context, client *http.Client, endpoint string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", liveStreamUserAgent)
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, stremioShelfMaxResponseSize+1))
	if err != nil {
		return err
	}
	if len(body) > stremioShelfMaxResponseSize {
		return fmt.Errorf("response exceeds %d bytes", stremioShelfMaxResponseSize)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("invalid JSON response: %w", err)
	}
	return nil
}

func normalizeStremioCatalogType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "movie", "movies":
		return "movie"
	case "series", "show", "shows", "tv":
		return "series"
	default:
		return ""
	}
}

func curatedItemFromStremioMeta(meta stremioMeta, fallbackType string) metadatapkg.CuratedItem {
	mediaType := normalizeStremioCatalogType(meta.Type)
	if mediaType == "" {
		mediaType = fallbackType
	}
	item := metadatapkg.CuratedItem{
		Title:     strings.TrimSpace(meta.Name),
		Year:      stremioReleaseYear(meta.ReleaseInfo),
		MediaType: mediaType,
	}
	id := strings.TrimSpace(meta.ID)
	if strings.HasPrefix(strings.ToLower(id), "tt") {
		item.IMDBID = id
	} else if strings.HasPrefix(strings.ToLower(id), "tmdb:") {
		parts := strings.Split(id, ":")
		if parsed, err := strconv.ParseInt(parts[len(parts)-1], 10, 64); err == nil && parsed > 0 {
			item.TMDBID = parsed
		}
	}
	return item
}

func stremioReleaseYear(value string) int {
	for _, field := range strings.FieldsFunc(value, func(r rune) bool { return r < '0' || r > '9' }) {
		if len(field) != 4 {
			continue
		}
		year, err := strconv.Atoi(field)
		if err == nil && year >= 1800 && year <= 2200 {
			return year
		}
	}
	return 0
}
