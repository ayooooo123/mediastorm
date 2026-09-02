package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/internal/netproxy"
)

const (
	stalkerDefaultModel     = "MAG254"
	stalkerUserAgent        = "Mozilla/5.0 (QtEmbedded; U; Linux; C) AppleWebKit/533.3 (KHTML, like Gecko) MAG200 stbapp ver: 4 rev: 2721 Mobile Safari/533.3"
	stalkerCatalogCacheTTL  = 30 * time.Minute
	stalkerResponseLimit    = 20 * 1024 * 1024
	stalkerHandshakeTimeout = 7 * time.Second
	stalkerProfileTimeout   = 3 * time.Second
)

type stalkerSourceConfig struct {
	PortalURL    string
	MAC          string
	SerialNumber string
	DeviceID     string
	DeviceID2    string
	Signature    string
	Model        string
	ProxyURL     string
}

type stalkerPortalSession struct {
	mu              sync.Mutex
	config          stalkerSourceConfig
	client          *http.Client
	endpoint        string
	token           string
	channels        []LiveChannel
	channelCommands map[string]string
	categoryPages   map[string]stalkerCachedChannelPage
	cacheExpiry     time.Time
}

type stalkerCachedChannelPage struct {
	data      []stalkerChannel
	total     int
	pageSize  int
	expiresAt time.Time
}

var stalkerSessionStore = struct {
	sync.Mutex
	sessions map[string]*stalkerPortalSession
}{sessions: make(map[string]*stalkerPortalSession)}

type stalkerEnvelope struct {
	JS json.RawMessage `json:"js"`
}

type stalkerHandshake struct {
	Token string `json:"token"`
}

type stalkerGenre struct {
	ID    flexString `json:"id"`
	Title string     `json:"title"`
}

type stalkerChannel struct {
	ID        flexString `json:"id"`
	Name      string     `json:"name"`
	Number    flexString `json:"number"`
	GenreID   flexString `json:"tv_genre_id"`
	Logo      string     `json:"logo"`
	Cmd       string     `json:"cmd"`
	XMLTVID   string     `json:"xmltv_id"`
	TVArchive flexString `json:"tv_archive"`
}

type stalkerCreateLink struct {
	Cmd   string `json:"cmd"`
	Error string `json:"error"`
}

type flexString string

func (s *flexString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = ""
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err == nil {
		*s = flexString(value)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	*s = flexString(number.String())
	return nil
}

func normalizedStalkerConfig(config stalkerSourceConfig) (stalkerSourceConfig, error) {
	config.PortalURL = strings.TrimSpace(config.PortalURL)
	config.MAC = strings.ToUpper(strings.TrimSpace(config.MAC))
	config.Model = strings.TrimSpace(config.Model)
	if config.Model == "" {
		config.Model = stalkerDefaultModel
	}
	parsed, err := url.Parse(config.PortalURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return config, errors.New("stalker portal URL must be an HTTP or HTTPS URL")
	}
	if parsed.User != nil {
		return config, errors.New("stalker portal URL must not contain embedded credentials")
	}
	if config.MAC == "" {
		return config, errors.New("stalker MAC address is required")
	}
	return config, nil
}

func stalkerSessionKey(config stalkerSourceConfig) string {
	identity := strings.Join([]string{
		strings.ToLower(config.PortalURL), config.MAC, config.SerialNumber,
		config.DeviceID, config.DeviceID2, config.Signature, config.Model, config.ProxyURL,
	}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

func getStalkerSession(config stalkerSourceConfig) (*stalkerPortalSession, error) {
	config, err := normalizedStalkerConfig(config)
	if err != nil {
		return nil, err
	}
	key := stalkerSessionKey(config)
	stalkerSessionStore.Lock()
	defer stalkerSessionStore.Unlock()
	if session := stalkerSessionStore.sessions[key]; session != nil {
		return session, nil
	}
	session, err := newStalkerSession(config)
	if err != nil {
		return nil, err
	}
	stalkerSessionStore.sessions[key] = session
	return session, nil
}

func newStalkerSession(config stalkerSourceConfig) (*stalkerPortalSession, error) {
	config, err := normalizedStalkerConfig(config)
	if err != nil {
		return nil, err
	}
	client, err := netproxy.NewHTTPClientWithOptions(netproxy.HTTPClientOptions{Timeout: defaultPlaylistTimeout, ResponseHeaderTimeout: defaultStreamOpenTimeout}, config.ProxyURL)
	if err != nil {
		return nil, fmt.Errorf("create stalker HTTP client: %w", err)
	}
	return &stalkerPortalSession{config: config, client: client, categoryPages: make(map[string]stalkerCachedChannelPage)}, nil
}

func stalkerEndpointCandidates(raw string) []string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return nil
	}
	u.RawQuery, u.Fragment = "", ""
	cleanPath := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(cleanPath, "/portal.php") || strings.HasSuffix(cleanPath, "/server/load.php") {
		u.Path = cleanPath
		return []string{u.String()}
	}
	basePath := cleanPath
	if strings.HasSuffix(basePath, "/c") {
		basePath = strings.TrimSuffix(basePath, "/c")
	}
	serverURL := *u
	serverURL.Path = strings.TrimRight(basePath, "/") + "/server/load.php"
	portalURL := *u
	portalURL.Path = path.Join(basePath, "portal.php")
	if basePath == "" || basePath == "/" {
		return []string{portalURL.String(), serverURL.String()}
	}
	return []string{serverURL.String(), portalURL.String()}
}

func (s *stalkerPortalSession) setHeaders(req *http.Request, authorized bool) {
	model := s.config.Model
	req.Header.Set("User-Agent", stalkerUserAgent)
	req.Header.Set("X-User-Agent", "Model: "+model+"; Link: Ethernet")
	req.Header.Set("Cookie", "mac="+url.QueryEscape(s.config.MAC)+"; stb_lang=en; timezone=UTC")
	if authorized && s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
}

func (s *stalkerPortalSession) request(ctx context.Context, endpoint string, params url.Values, authorized bool) (json.RawMessage, int, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, 0, err
	}
	query := parsed.Query()
	for key, values := range params {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	if query.Get("JsHttpRequest") == "" {
		query.Set("JsHttpRequest", "1-xml")
	}
	parsed.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	s.setHeaders(req, authorized)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, stalkerResponseLimit+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if len(body) > stalkerResponseLimit {
		return nil, resp.StatusCode, errors.New("stalker response exceeds size limit")
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, resp.StatusCode, fmt.Errorf("stalker portal returned status %d", resp.StatusCode)
	}
	var envelope stalkerEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("decode stalker response: %w", err)
	}
	if len(envelope.JS) == 0 || string(envelope.JS) == "null" {
		return nil, resp.StatusCode, errors.New("stalker portal returned an empty response")
	}
	return envelope.JS, resp.StatusCode, nil
}

func (s *stalkerPortalSession) handshakeLocked(ctx context.Context) error {
	var lastErr error
	for _, endpoint := range stalkerEndpointCandidates(s.config.PortalURL) {
		attemptCtx, cancel := context.WithTimeout(ctx, stalkerHandshakeTimeout)
		js, _, err := s.request(attemptCtx, endpoint, url.Values{"type": {"stb"}, "action": {"handshake"}, "token": {""}}, false)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		var handshake stalkerHandshake
		if err := json.Unmarshal(js, &handshake); err != nil || strings.TrimSpace(handshake.Token) == "" {
			lastErr = errors.New("stalker handshake did not return a token")
			continue
		}
		s.endpoint = endpoint
		s.token = strings.TrimSpace(handshake.Token)
		profileParams := url.Values{
			"type": {"stb"}, "action": {"get_profile"}, "stb_type": {s.config.Model},
			"auth_second_step": {"1"}, "hd": {"1"}, "num_banks": {"2"},
			"image_version": {"218"}, "hw_version": {"1.7-BD-00"}, "client_type": {"STB"},
		}
		if s.config.SerialNumber != "" {
			profileParams.Set("sn", s.config.SerialNumber)
		}
		if s.config.DeviceID != "" {
			profileParams.Set("device_id", s.config.DeviceID)
		}
		if s.config.DeviceID2 != "" {
			profileParams.Set("device_id2", s.config.DeviceID2)
		}
		if s.config.Signature != "" {
			profileParams.Set("signature", s.config.Signature)
		}
		profileCtx, cancelProfile := context.WithTimeout(ctx, stalkerProfileTimeout)
		_, _, _ = s.request(profileCtx, s.endpoint, profileParams, true)
		cancelProfile()
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("no stalker portal endpoint candidates")
	}
	return lastErr
}

// probeStalkerPortal deliberately uses a fresh session and a lightweight API
// request. Admin connection tests should not wait behind a catalogue refresh or
// download every channel merely to prove that the portal credentials work.
func probeStalkerPortal(ctx context.Context, config stalkerSourceConfig) (int, error) {
	session, err := newStalkerSession(config)
	if err != nil {
		return 0, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	genresRaw, err := session.authorizedRequestLocked(ctx, url.Values{"type": {"itv"}, "action": {"get_genres"}})
	if err != nil {
		return 0, fmt.Errorf("authenticate with portal: %w", err)
	}
	var genres []stalkerGenre
	if err := decodeStalkerList(genresRaw, &genres); err != nil {
		return 0, fmt.Errorf("read portal genres: %w", err)
	}
	return len(genres), nil
}

func stalkerResponseUnauthorized(js json.RawMessage) bool {
	lower := strings.ToLower(string(js))
	return strings.Contains(lower, "authorization") || strings.Contains(lower, "not valid token") || strings.Contains(lower, "invalid token")
}

func (s *stalkerPortalSession) authorizedRequestLocked(ctx context.Context, params url.Values) (json.RawMessage, error) {
	if s.token == "" || s.endpoint == "" {
		if err := s.handshakeLocked(ctx); err != nil {
			return nil, err
		}
	}
	js, status, err := s.request(ctx, s.endpoint, params, true)
	if err == nil && !stalkerResponseUnauthorized(js) {
		return js, nil
	}
	if status != http.StatusUnauthorized && status != http.StatusForbidden && err != nil && !stalkerResponseUnauthorized(js) {
		return nil, err
	}
	s.token = ""
	if handshakeErr := s.handshakeLocked(ctx); handshakeErr != nil {
		return nil, handshakeErr
	}
	js, _, err = s.request(ctx, s.endpoint, params, true)
	return js, err
}

func decodeStalkerList(raw json.RawMessage, out any) error {
	if err := json.Unmarshal(raw, out); err == nil {
		return nil
	}
	var wrapper struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil || len(wrapper.Data) == 0 {
		return errors.New("stalker portal returned an invalid list")
	}
	return json.Unmarshal(wrapper.Data, out)
}

func (s *stalkerPortalSession) fetchPortalChannelsLocked(ctx context.Context) ([]stalkerChannel, error) {
	channelsRaw, err := s.authorizedRequestLocked(ctx, url.Values{"type": {"itv"}, "action": {"get_all_channels"}})
	var channels []stalkerChannel
	if err == nil {
		err = decodeStalkerList(channelsRaw, &channels)
	}
	if err == nil && len(channels) > 0 {
		return channels, nil
	}

	// Some Ministra forks omit get_all_channels and expose only the paginated
	// ordered-list action used by MAG firmware.
	channels = nil
	for pageNumber := 1; pageNumber <= 100; pageNumber++ {
		pageRaw, pageErr := s.authorizedRequestLocked(ctx, url.Values{
			"type": {"itv"}, "action": {"get_ordered_list"}, "genre": {"*"},
			"p": {fmt.Sprintf("%d", pageNumber)}, "sortby": {"number"}, "fav": {"0"}, "hd": {"0"},
		})
		if pageErr != nil {
			if len(channels) > 0 {
				return channels, nil
			}
			if err != nil {
				return nil, err
			}
			return nil, pageErr
		}
		var page struct {
			Data         []stalkerChannel `json:"data"`
			TotalItems   flexString       `json:"total_items"`
			MaxPageItems flexString       `json:"max_page_items"`
		}
		if unmarshalErr := json.Unmarshal(pageRaw, &page); unmarshalErr != nil {
			return nil, unmarshalErr
		}
		channels = append(channels, page.Data...)
		if len(page.Data) == 0 {
			break
		}
		total, _ := strconv.Atoi(string(page.TotalItems))
		if total > 0 && len(channels) >= total {
			break
		}
		pageSize, _ := strconv.Atoi(string(page.MaxPageItems))
		if pageSize > 0 && len(page.Data) < pageSize {
			break
		}
	}
	return channels, nil
}

func (s *stalkerPortalSession) fetchOrderedChannelPageLocked(ctx context.Context, genreID string, pageNumber int) (stalkerCachedChannelPage, error) {
	if pageNumber < 1 {
		pageNumber = 1
	}
	cacheKey := genreID + ":" + strconv.Itoa(pageNumber)
	if cached, ok := s.categoryPages[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		cached.data = append([]stalkerChannel(nil), cached.data...)
		return cached, nil
	}
	pageRaw, err := s.authorizedRequestLocked(ctx, url.Values{
		"type": {"itv"}, "action": {"get_ordered_list"}, "genre": {genreID},
		"p": {strconv.Itoa(pageNumber)}, "sortby": {"number"}, "fav": {"0"}, "hd": {"0"},
	})
	if err != nil {
		return stalkerCachedChannelPage{}, err
	}
	var response struct {
		Data         []stalkerChannel `json:"data"`
		TotalItems   flexString       `json:"total_items"`
		MaxPageItems flexString       `json:"max_page_items"`
	}
	if err := json.Unmarshal(pageRaw, &response); err != nil {
		return stalkerCachedChannelPage{}, err
	}
	total, _ := strconv.Atoi(string(response.TotalItems))
	pageSize, _ := strconv.Atoi(string(response.MaxPageItems))
	if pageSize <= 0 {
		pageSize = len(response.Data)
	}
	page := stalkerCachedChannelPage{
		data: append([]stalkerChannel(nil), response.Data...), total: total, pageSize: pageSize,
		expiresAt: time.Now().Add(stalkerCatalogCacheTTL),
	}
	s.categoryPages[cacheKey] = page
	return page, nil
}

// channelsForCategories returns the first maxItems channels across the named
// portal genres plus the provider-reported total. GetChannels uses the total
// for frontend paging, so a category can reach beyond get_all_channels without
// downloading the portal's entire live catalogue.
func (s *stalkerPortalSession) channelsForCategories(ctx context.Context, categories []string, maxItems int) ([]LiveChannel, int, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxItems <= 0 {
		return nil, 0, nil, nil
	}
	genresRaw, err := s.authorizedRequestLocked(ctx, url.Values{"type": {"itv"}, "action": {"get_genres"}})
	if err != nil {
		return nil, 0, nil, fmt.Errorf("fetch stalker genres: %w", err)
	}
	var genres []stalkerGenre
	if err := decodeStalkerList(genresRaw, &genres); err != nil {
		return nil, 0, nil, fmt.Errorf("decode stalker genres: %w", err)
	}
	genreIDs := make(map[string]string, len(genres))
	genreNames := make(map[string]string, len(genres))
	availableCategories := make([]string, 0, len(genres))
	for _, genre := range genres {
		name := strings.TrimSpace(genre.Title)
		id := strings.TrimSpace(string(genre.ID))
		if name != "" && id != "" {
			genreIDs[strings.ToLower(name)] = id
			genreNames[id] = name
			availableCategories = append(availableCategories, name)
		}
	}
	requestedCategories := categories
	if len(requestedCategories) == 0 {
		requestedCategories = []string{"*"}
		genreIDs["*"] = "*"
	}

	var rawChannels []stalkerChannel
	total := 0
	for _, category := range requestedCategories {
		genreID := genreIDs[strings.ToLower(strings.TrimSpace(category))]
		if genreID == "" {
			continue
		}
		firstPage, err := s.fetchOrderedChannelPageLocked(ctx, genreID, 1)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("fetch stalker category %q: %w", category, err)
		}
		categoryTotal := firstPage.total
		if categoryTotal <= 0 {
			categoryTotal = len(firstPage.data)
		}
		total += categoryTotal
		remaining := maxItems - len(rawChannels)
		if remaining <= 0 {
			continue
		}
		rawChannels = append(rawChannels, firstPage.data[:min(len(firstPage.data), remaining)]...)
		pageSize := firstPage.pageSize
		if pageSize <= 0 {
			continue
		}
		for pageNumber := 2; len(rawChannels) < maxItems && (pageNumber-1)*pageSize < categoryTotal; pageNumber++ {
			page, err := s.fetchOrderedChannelPageLocked(ctx, genreID, pageNumber)
			if err != nil {
				return nil, 0, nil, fmt.Errorf("fetch stalker category %q page %d: %w", category, pageNumber, err)
			}
			if len(page.data) == 0 {
				break
			}
			remaining = maxItems - len(rawChannels)
			rawChannels = append(rawChannels, page.data[:min(len(page.data), remaining)]...)
		}
	}
	return s.convertPortalChannelsLocked(rawChannels, genreNames), total, availableCategories, nil
}

func (s *stalkerPortalSession) convertPortalChannelsLocked(portalChannels []stalkerChannel, genreNames map[string]string) []LiveChannel {
	channels := make([]LiveChannel, 0, len(portalChannels))
	if s.channelCommands == nil {
		s.channelCommands = make(map[string]string, len(portalChannels))
	}
	for _, channel := range portalChannels {
		id := strings.TrimSpace(string(channel.ID))
		if id == "" || strings.TrimSpace(channel.Name) == "" {
			continue
		}
		logo := strings.TrimSpace(channel.Logo)
		if logo != "" {
			if parsed, parseErr := url.Parse(logo); parseErr == nil && !parsed.IsAbs() {
				if base, baseErr := url.Parse(s.endpoint); baseErr == nil {
					logo = base.ResolveReference(parsed).String()
				}
			}
		}
		tvgID := strings.TrimSpace(channel.XMLTVID)
		if tvgID == "" {
			tvgID = id
		}
		channels = append(channels, LiveChannel{
			ID: id, PlaybackID: id, Name: strings.TrimSpace(channel.Name), URL: s.config.PortalURL,
			Logo: logo, Group: genreNames[string(channel.GenreID)], TvgID: tvgID, TvgName: strings.TrimSpace(channel.Name),
		})
		s.channelCommands[id] = strings.TrimSpace(channel.Cmd)
	}
	return channels
}

func (s *stalkerPortalSession) channelsForPortal(ctx context.Context) ([]LiveChannel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.channels) > 0 && time.Now().Before(s.cacheExpiry) {
		return append([]LiveChannel(nil), s.channels...), nil
	}
	genresRaw, err := s.authorizedRequestLocked(ctx, url.Values{"type": {"itv"}, "action": {"get_genres"}})
	if err != nil {
		return nil, fmt.Errorf("fetch stalker genres: %w", err)
	}
	var genres []stalkerGenre
	if err := decodeStalkerList(genresRaw, &genres); err != nil {
		return nil, fmt.Errorf("decode stalker genres: %w", err)
	}
	genreNames := make(map[string]string, len(genres))
	for _, genre := range genres {
		genreNames[string(genre.ID)] = strings.TrimSpace(genre.Title)
	}
	portalChannels, err := s.fetchPortalChannelsLocked(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch stalker channels: %w", err)
	}
	channels := make([]LiveChannel, 0, len(portalChannels))
	commands := make(map[string]string, len(portalChannels))
	for _, channel := range portalChannels {
		id := strings.TrimSpace(string(channel.ID))
		if id == "" || strings.TrimSpace(channel.Name) == "" {
			continue
		}
		logo := strings.TrimSpace(channel.Logo)
		if logo != "" {
			if parsed, parseErr := url.Parse(logo); parseErr == nil && !parsed.IsAbs() {
				if base, baseErr := url.Parse(s.endpoint); baseErr == nil {
					logo = base.ResolveReference(parsed).String()
				}
			}
		}
		tvgID := strings.TrimSpace(channel.XMLTVID)
		if tvgID == "" {
			tvgID = id
		}
		channels = append(channels, LiveChannel{
			ID: id, PlaybackID: id, Name: strings.TrimSpace(channel.Name), URL: s.config.PortalURL,
			Logo: logo, Group: genreNames[string(channel.GenreID)], TvgID: tvgID, TvgName: strings.TrimSpace(channel.Name),
		})
		commands[id] = strings.TrimSpace(channel.Cmd)
	}
	s.channels = channels
	s.channelCommands = commands
	s.cacheExpiry = time.Now().Add(stalkerCatalogCacheTTL)
	return append([]LiveChannel(nil), channels...), nil
}

func cleanStalkerStreamCommand(command string) (string, error) {
	command = strings.TrimSpace(command)
	for _, prefix := range []string{"ffmpeg ", "ffrt ", "auto "} {
		if strings.HasPrefix(strings.ToLower(command), prefix) {
			command = strings.TrimSpace(command[len(prefix):])
			break
		}
	}
	if fields := strings.Fields(command); len(fields) > 1 {
		for _, field := range fields {
			if strings.HasPrefix(field, "http://") || strings.HasPrefix(field, "https://") {
				command = field
				break
			}
		}
	}
	parsed, err := url.Parse(command)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("stalker portal returned an invalid stream URL")
	}
	if values, exists := parsed.Query()["stream"]; exists && (len(values) == 0 || strings.TrimSpace(values[0]) == "") {
		return "", errors.New("stalker portal returned a stream URL without a stream ID")
	}
	return parsed.String(), nil
}

func directStalkerStreamURL(command string) (string, bool) {
	streamURL, err := cleanStalkerStreamCommand(command)
	if err != nil {
		return "", false
	}
	parsed, err := url.Parse(streamURL)
	if err != nil {
		return "", false
	}
	values, exists := parsed.Query()["stream"]
	return streamURL, exists && len(values) > 0 && strings.TrimSpace(values[0]) != ""
}

func (s *stalkerPortalSession) resolveChannel(ctx context.Context, channelID string) (string, map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return "", nil, errors.New("stalker channel ID is required")
	}
	cmd := strings.TrimSpace(s.channelCommands[channelID])
	if cmd == "" {
		// The catalog may not have been loaded in this process (for example, after
		// a restart followed by a recording). Refresh it before resolving.
		channels, fetchErr := s.fetchPortalChannelsLocked(ctx)
		if fetchErr == nil {
			if s.channelCommands == nil {
				s.channelCommands = make(map[string]string, len(channels))
			}
			for _, channel := range channels {
				s.channelCommands[string(channel.ID)] = strings.TrimSpace(channel.Cmd)
			}
			cmd = strings.TrimSpace(s.channelCommands[channelID])
		}
	}
	if cmd == "" {
		return "", nil, errors.New("stalker channel was not found in the portal catalog")
	}
	headers := map[string]string{
		"User-Agent":   stalkerUserAgent,
		"X-User-Agent": "Model: " + s.config.Model + "; Link: Ethernet",
		"Cookie":       "mac=" + url.QueryEscape(s.config.MAC) + "; stb_lang=en; timezone=UTC",
	}
	// Many reseller portals return fully resolved play/live.php commands from
	// get_all_channels. Sending those through create_link again can erase the
	// stream ID, so only temporary /ch/... commands need portal resolution.
	if streamURL, direct := directStalkerStreamURL(cmd); direct {
		return streamURL, headers, nil
	}

	var lastErr error
	for _, forceCheck := range []string{"0", "1"} {
		js, err := s.authorizedRequestLocked(ctx, url.Values{
			"type": {"itv"}, "action": {"create_link"}, "cmd": {cmd},
			"series": {""}, "forced_storage": {"undefined"}, "disable_ad": {"0"}, "download": {"0"},
			"force_ch_link_check": {forceCheck},
		})
		if err != nil {
			lastErr = err
			continue
		}
		var link stalkerCreateLink
		if err := json.Unmarshal(js, &link); err != nil {
			lastErr = fmt.Errorf("decode stalker stream link: %w", err)
			continue
		}
		if strings.TrimSpace(link.Error) != "" {
			lastErr = fmt.Errorf("portal reported %s", strings.TrimSpace(link.Error))
			continue
		}
		streamURL, err := cleanStalkerStreamCommand(link.Cmd)
		if err != nil {
			lastErr = err
			continue
		}
		return streamURL, headers, nil
	}
	if lastErr == nil {
		lastErr = errors.New("portal returned no playable link")
	}
	return "", nil, fmt.Errorf("create stalker stream link: %w", lastErr)
}

func fetchStalkerChannels(ctx context.Context, config stalkerSourceConfig) ([]LiveChannel, error) {
	session, err := getStalkerSession(config)
	if err != nil {
		return nil, err
	}
	return session.channelsForPortal(ctx)
}

func fetchStalkerCategoryChannels(ctx context.Context, config stalkerSourceConfig, categories []string, maxItems int) ([]LiveChannel, int, []string, error) {
	session, err := getStalkerSession(config)
	if err != nil {
		return nil, 0, nil, err
	}
	return session.channelsForCategories(ctx, categories, maxItems)
}

func resolveStalkerChannel(ctx context.Context, config stalkerSourceConfig, channelID string) (string, map[string]string, error) {
	session, err := getStalkerSession(config)
	if err != nil {
		return "", nil, err
	}
	return session.resolveChannel(ctx, channelID)
}
