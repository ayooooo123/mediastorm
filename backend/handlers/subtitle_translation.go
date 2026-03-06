package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const googleTranslateURL = "https://translate.googleapis.com/translate_a/single"
const translateDelimiter = "|||STRMR|||"

var (
	vttMarkupTagPattern = regexp.MustCompile(`</?[^>]+>`)
	whitespacePattern   = regexp.MustCompile(`\s+`)
)

type subtitleTranslator interface {
	TranslateBatch(ctx context.Context, texts []string, sourceLang, targetLang string) ([]string, error)
}

type googleUnofficialTranslator struct {
	client  *http.Client
	limiter *rate.Limiter
}

func newGoogleUnofficialTranslator() *googleUnofficialTranslator {
	return &googleUnofficialTranslator{
		client: &http.Client{Timeout: 20 * time.Second},
		// Lightweight request pacing to reduce throttling/rate-limit spikes.
		limiter: rate.NewLimiter(rate.Every(150*time.Millisecond), 1),
	}
}

func (t *googleUnofficialTranslator) TranslateBatch(ctx context.Context, texts []string, sourceLang, targetLang string) ([]string, error) {
	if len(texts) == 0 {
		return []string{}, nil
	}
	if strings.TrimSpace(targetLang) == "" {
		return nil, errors.New("target language is required")
	}
	if strings.TrimSpace(sourceLang) == "" {
		sourceLang = "en"
	}

	joined := strings.Join(texts, "\n"+translateDelimiter+"\n")
	translatedJoined, err := t.translateTextWithRetry(ctx, joined, sourceLang, targetLang)
	if err != nil {
		return nil, err
	}

	parts := strings.Split(translatedJoined, translateDelimiter)
	if len(parts) == len(texts) {
		out := make([]string, len(parts))
		for i := range parts {
			out[i] = strings.TrimSpace(parts[i])
		}
		return out, nil
	}

	// Fallback to per-line translation if delimiter was modified by translation.
	out := make([]string, 0, len(texts))
	for _, text := range texts {
		line, lineErr := t.translateTextWithRetry(ctx, text, sourceLang, targetLang)
		if lineErr != nil {
			return nil, lineErr
		}
		out = append(out, line)
	}
	return out, nil
}

func (t *googleUnofficialTranslator) translateTextWithRetry(ctx context.Context, text, sourceLang, targetLang string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", nil
	}

	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if err := t.limiter.Wait(ctx); err != nil {
			return "", err
		}
		translated, retryable, err := t.translateTextOnce(ctx, text, sourceLang, targetLang)
		if err == nil {
			return translated, nil
		}
		lastErr = err
		if !retryable {
			break
		}
		backoff := time.Duration(250*(1<<attempt)) * time.Millisecond
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(backoff):
		}
	}
	if lastErr == nil {
		lastErr = errors.New("translation failed")
	}
	return "", lastErr
}

func (t *googleUnofficialTranslator) translateTextOnce(ctx context.Context, text, sourceLang, targetLang string) (translated string, retryable bool, err error) {
	u, err := url.Parse(googleTranslateURL)
	if err != nil {
		return "", false, err
	}

	q := u.Query()
	q.Set("client", "gtx")
	q.Set("sl", sourceLang)
	q.Set("tl", targetLang)
	q.Set("dt", "t")
	q.Set("q", text)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", false, err
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return "", true, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		statusErr := fmt.Errorf("translate API status=%d body=%q", resp.StatusCode, string(body))
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return "", true, statusErr
		}
		return "", false, statusErr
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", true, err
	}

	var data []any
	if err := json.Unmarshal(body, &data); err != nil {
		return "", false, err
	}
	if len(data) == 0 {
		return "", false, errors.New("empty translation response")
	}

	segments, ok := data[0].([]any)
	if !ok {
		return "", false, errors.New("unexpected translation response format")
	}

	var b strings.Builder
	for _, segment := range segments {
		segmentParts, ok := segment.([]any)
		if !ok || len(segmentParts) == 0 {
			continue
		}
		part, ok := segmentParts[0].(string)
		if !ok {
			continue
		}
		b.WriteString(part)
	}

	if b.Len() == 0 {
		return "", false, errors.New("translation response had no text")
	}

	return b.String(), false, nil
}

type subtitleTranslationManager struct {
	translator subtitleTranslator
	cacheDir   string
	httpClient *http.Client
}

func newSubtitleTranslationManager(cacheDir string, translator subtitleTranslator) *subtitleTranslationManager {
	if strings.TrimSpace(cacheDir) == "" {
		cacheDir = filepath.Join(os.TempDir(), "strmr-subtitles", "translated")
	}
	_ = os.MkdirAll(cacheDir, 0o755)
	return &subtitleTranslationManager{
		translator: translator,
		cacheDir:   cacheDir,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (m *subtitleTranslationManager) TranslateVTTFromURL(ctx context.Context, sourceURL, userID, sourceLang, targetLang, authHeader string) ([]byte, error) {
	cachePath := m.translationCachePath(sourceURL, userID, sourceLang, targetLang)
	snapshotPath := translationSnapshotPath(cachePath)
	cachedLines, _ := readTranslationCache(cachePath)
	content, err := m.fetchSourceVTT(ctx, sourceURL, authHeader)
	if err != nil {
		// If source subtitle session expired/unavailable, serve the latest translated
		// snapshot from cache so playback can continue with previously translated subs.
		if snapshot, readErr := os.ReadFile(snapshotPath); readErr == nil && len(snapshot) > 0 {
			log.Printf("[subtitles] source fetch failed, serving cached translated snapshot %q: %v", snapshotPath, err)
			return snapshot, nil
		}
		return nil, err
	}

	translated, updatedCache, err := translateVTTContentIncremental(
		ctx,
		string(content),
		sourceLang,
		targetLang,
		m.translator,
		cachedLines,
		3*time.Second,
	)
	if err != nil {
		return nil, err
	}

	if err := writeTranslationCache(cachePath, updatedCache); err != nil {
		log.Printf("[subtitles] failed writing translation cache %q: %v", cachePath, err)
	}
	if err := writeFileAtomic(snapshotPath, []byte(translated)); err != nil {
		log.Printf("[subtitles] failed writing translation snapshot %q: %v", snapshotPath, err)
	}

	return []byte(translated), nil
}

func translationSnapshotPath(cachePath string) string {
	return strings.TrimSuffix(cachePath, filepath.Ext(cachePath)) + ".vtt"
}

func (m *subtitleTranslationManager) fetchSourceVTT(ctx context.Context, sourceURL, authHeader string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(authHeader) != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("source subtitle fetch failed status=%d body=%q", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, errors.New("source subtitle content is empty")
	}
	return body, nil
}

func (m *subtitleTranslationManager) translationCachePath(sourceURL, userID, sourceLang, targetLang string) string {
	if strings.TrimSpace(userID) == "" {
		userID = "anonymous"
	}
	raw := strings.Join([]string{userID, sourceLang, targetLang, sourceURL}, "|")
	sum := sha256.Sum256([]byte(raw))
	return filepath.Join(m.cacheDir, hex.EncodeToString(sum[:])+".json")
}

func translateVTTContentIncremental(
	ctx context.Context,
	content, sourceLang, targetLang string,
	tr subtitleTranslator,
	cached map[string]string,
	timeBudget time.Duration,
) (string, map[string]string, error) {
	if cached == nil {
		cached = map[string]string{}
	}
	if timeBudget <= 0 {
		timeBudget = 30 * time.Second
	}
	deadline := time.Now().Add(timeBudget)
	lines := strings.Split(content, "\n")

	type candidate struct {
		idx   int
		text  string
		cache string
	}
	candidates := make([]candidate, 0, len(lines))
	for i := range lines {
		if !isTranslatableVTTLine(lines, i) {
			continue
		}

		cleanLine := sanitizeVTTTranslationLine(lines[i])
		lines[i] = cleanLine
		if cleanLine == "" {
			continue
		}

		key := lineCacheKey(cleanLine, sourceLang, targetLang)
		if translated, ok := cached[key]; ok {
			lines[i] = translated
			continue
		}
		candidates = append(candidates, candidate{idx: i, text: cleanLine, cache: key})
	}
	if len(candidates) == 0 {
		if strings.HasPrefix(strings.TrimSpace(content), "WEBVTT") {
			return content, cached, nil
		}
		return "WEBVTT\n\n" + content, cached, nil
	}

	const batchSize = 80
	for start := 0; start < len(candidates); start += batchSize {
		// Stop translating new batches if we've exceeded our time budget.
		// Already-cached lines are applied above, so partial results are valid VTT.
		if time.Now().After(deadline) {
			log.Printf("[subtitles] translation time budget (%.0fs) exceeded after %d/%d lines, returning partial result",
				timeBudget.Seconds(), start, len(candidates))
			break
		}
		end := start + batchSize
		if end > len(candidates) {
			end = len(candidates)
		}
		batch := candidates[start:end]
		batchTexts := make([]string, 0, len(batch))
		for _, c := range batch {
			batchTexts = append(batchTexts, c.text)
		}
		translated, err := tr.TranslateBatch(ctx, batchTexts, sourceLang, targetLang)
		if err != nil {
			// If we already translated some batches, return partial result
			// rather than failing the entire request.
			if start > 0 {
				log.Printf("[subtitles] translation error after %d/%d lines, returning partial result: %v",
					start, len(candidates), err)
				break
			}
			return "", cached, err
		}
		if len(translated) != len(batch) {
			if start > 0 {
				log.Printf("[subtitles] translated line count mismatch after %d/%d lines, returning partial result",
					start, len(candidates))
				break
			}
			return "", cached, fmt.Errorf("translated line count mismatch: got=%d want=%d", len(translated), len(batch))
		}
		for i, c := range batch {
			lines[c.idx] = translated[i]
			cached[c.cache] = translated[i]
		}
	}

	translatedContent := strings.Join(lines, "\n")
	if !strings.HasPrefix(strings.TrimSpace(translatedContent), "WEBVTT") {
		translatedContent = "WEBVTT\n\n" + translatedContent
	}
	return translatedContent, cached, nil
}

func lineCacheKey(text, sourceLang, targetLang string) string {
	raw := strings.Join([]string{sourceLang, targetLang, text}, "|")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func sanitizeVTTTranslationLine(line string) string {
	text := strings.TrimSpace(line)
	if text == "" {
		return ""
	}

	text = vttMarkupTagPattern.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, `\N`, " ")
	text = whitespacePattern.ReplaceAllString(strings.TrimSpace(text), " ")
	if text == "" {
		return ""
	}
	if looksLikeASSVectorPath(text) {
		return ""
	}
	return text
}

func looksLikeASSVectorPath(text string) bool {
	tokens := strings.Fields(strings.ToLower(strings.TrimSpace(text)))
	if len(tokens) < 6 {
		return false
	}

	commandCount := 0
	numericCount := 0
	for _, token := range tokens {
		switch token {
		case "m", "n", "l", "b", "s", "p", "c":
			commandCount++
			continue
		}
		if _, err := strconv.ParseFloat(token, 64); err == nil {
			numericCount++
			continue
		}
		return false
	}
	return commandCount >= 1 && numericCount >= 4
}

func readTranslationCache(path string) (map[string]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out map[string]string
	if err := json.Unmarshal(content, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func writeTranslationCache(path string, cache map[string]string) error {
	if cache == nil {
		cache = map[string]string{}
	}
	encoded, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, encoded)
}

func isTranslatableVTTLine(lines []string, idx int) bool {
	line := strings.TrimSpace(lines[idx])
	if line == "" {
		return false
	}
	if line == "WEBVTT" || strings.HasPrefix(line, "WEBVTT ") {
		return false
	}
	if strings.Contains(line, "-->") {
		return false
	}
	if strings.HasPrefix(line, "NOTE") || strings.HasPrefix(line, "STYLE") || strings.HasPrefix(line, "REGION") {
		return false
	}
	if strings.HasPrefix(line, "X-TIMESTAMP-MAP=") {
		return false
	}
	// Cue identifier lines are optional lines directly before a timestamp
	// and typically follow a blank separator line. Do not skip subtitle text lines.
	next := nextNonEmptyLine(lines, idx+1)
	if next != "" && strings.Contains(next, "-->") {
		prev := ""
		if idx > 0 {
			prev = strings.TrimSpace(lines[idx-1])
		}
		if idx == 0 || prev == "" {
			return false
		}
	}
	return true
}

func nextNonEmptyLine(lines []string, from int) string {
	for i := from; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}

func writeFileAtomic(path string, content []byte) error {
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
