package handlers

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const thumbnailSourcePrewarmTimeout = 15 * time.Second
const thumbnailSourcePrewarmTTL = 5 * time.Minute

// thumbnailSourceBridge keeps one upstream HTTP transport alive for all frame
// extraction processes. Stable FFmpeg releases do not yet have the shared:
// protocol, so this removes repeated DNS/TLS/redirect setup without tying the
// backend to libavformat's C ABI.
type thumbnailSourceBridge struct {
	mu       sync.RWMutex
	sessions map[string]thumbnailSourceSession
	client   *http.Client
	once     sync.Once
	baseURL  string
	secret   string
	err      error
}

type thumbnailSourceSession struct {
	sourceURL  string
	authHeader string
}

func newThumbnailSourceBridge() *thumbnailSourceBridge {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 32
	transport.MaxIdleConnsPerHost = 16
	transport.IdleConnTimeout = 2 * time.Minute
	return &thumbnailSourceBridge{
		sessions: make(map[string]thumbnailSourceSession),
		client: &http.Client{
			Transport:     transport,
			CheckRedirect: thumbnailRedirectPolicy,
		},
	}
}

func thumbnailRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("too many redirects")
	}
	if len(via) == 0 || req == nil || req.URL == nil || via[0] == nil || via[0].URL == nil {
		return nil
	}
	// WebDAV servers commonly redirect within their own origin and still require
	// Basic auth on the redirected request. Preserve that behavior without
	// forwarding credentials to a CDN or any other cross-origin destination.
	if strings.EqualFold(req.URL.Scheme, via[0].URL.Scheme) && strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		if authorization := via[0].Header.Get("Authorization"); authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
	}
	return nil
}

func (b *thumbnailSourceBridge) register(key, sourceURL, authHeader string) (string, error) {
	if b == nil {
		return "", fmt.Errorf("thumbnail source bridge unavailable")
	}
	b.once.Do(b.start)
	if b.err != nil {
		return "", b.err
	}
	b.mu.Lock()
	b.sessions[key] = thumbnailSourceSession{sourceURL: sourceURL, authHeader: authHeader}
	b.mu.Unlock()
	return b.baseURL + "/" + key, nil
}

func (b *thumbnailSourceBridge) start() {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.err = fmt.Errorf("listen for thumbnail source bridge: %w", err)
		return
	}
	secretBytes := make([]byte, 18)
	if _, err := rand.Read(secretBytes); err != nil {
		_ = listener.Close()
		b.err = fmt.Errorf("create thumbnail source bridge secret: %w", err)
		return
	}
	b.secret = hex.EncodeToString(secretBytes)
	b.baseURL = "http://" + listener.Addr().String() + "/thumbnail-source/" + b.secret
	server := &http.Server{
		Handler:           http.HandlerFunc(b.serveHTTP),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("[thumbnails] source bridge stopped: %v", err)
		}
	}()
}

func (b *thumbnailSourceBridge) serveHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "thumbnail-source" || subtle.ConstantTimeCompare([]byte(parts[1]), []byte(b.secret)) != 1 || !validThumbnailKey(parts[2]) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	b.mu.RLock()
	session, ok := b.sessions[parts[2]]
	b.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	upstream, err := http.NewRequestWithContext(r.Context(), r.Method, session.sourceURL, nil)
	if err != nil {
		http.Error(w, "invalid thumbnail source", http.StatusBadGateway)
		return
	}
	for _, name := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		if value := r.Header.Get(name); value != "" {
			upstream.Header.Set(name, value)
		}
	}
	applyRawHTTPHeaders(upstream.Header, session.authHeader)
	response, err := b.client.Do(upstream)
	if err != nil {
		http.Error(w, "thumbnail source unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for _, name := range []string{"Accept-Ranges", "Cache-Control", "Content-Length", "Content-Range", "Content-Type", "ETag", "Last-Modified"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, response.Body)
	}
}

func applyRawHTTPHeaders(headers http.Header, raw string) {
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(name) != "" {
			headers.Set(strings.TrimSpace(name), strings.TrimSpace(value))
		}
	}
}

func (b *thumbnailSourceBridge) prewarm(ctx context.Context, key, sourceURL, authHeader string) error {
	if _, err := b.register(key, sourceURL, authHeader); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, sourceURL, nil)
	if err != nil {
		return err
	}
	applyRawHTTPHeaders(req.Header, authHeader)
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("source returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func detectFFmpegSharedProtocol(ctx context.Context, ffmpegPath string) (bool, error) {
	path := strings.TrimSpace(ffmpegPath)
	if path == "" {
		path = "ffmpeg"
	}
	output, err := exec.CommandContext(ctx, path, "-hide_banner", "-protocols").CombinedOutput()
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == "shared" {
			return true, nil
		}
	}
	return false, nil
}

func detectFFmpegHTTPInputOptions(ctx context.Context, ffmpegPath string) ([]string, error) {
	path := strings.TrimSpace(ffmpegPath)
	if path == "" {
		path = "ffmpeg"
	}
	output, err := exec.CommandContext(ctx, path, "-hide_banner", "-h", "protocol=http").CombinedOutput()
	if err != nil {
		return nil, err
	}
	available := string(output)
	options := make([]string, 0, 8)
	if strings.Contains(available, "-multiple_requests") {
		options = append(options, "-multiple_requests", "1")
	}
	if strings.Contains(available, "-short_seek_size") {
		options = append(options, "-short_seek_size", "1048576")
	}
	if strings.Contains(available, "-initial_request_size") {
		options = append(options, "-initial_request_size", "2097152")
	}
	if strings.Contains(available, "-request_size") {
		options = append(options, "-request_size", "33554432")
	}
	return options, nil
}

func (m *ThumbnailManager) sharedProtocolAvailable() bool {
	m.protocolOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m.sharedProtocol, m.protocolErr = detectFFmpegSharedProtocol(ctx, m.ffmpegPath)
		if m.protocolErr != nil {
			log.Printf("[thumbnails] unable to inspect ffmpeg protocols: %v", m.protocolErr)
		}
		if m.sharedProtocol {
			log.Printf("[thumbnails] native ffmpeg shared source cache enabled")
		}
	})
	return m.sharedProtocol
}

func (m *ThumbnailManager) ffmpegHTTPOptions() []string {
	m.httpOptionsOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		options, err := detectFFmpegHTTPInputOptions(ctx, m.ffmpegPath)
		if err != nil {
			log.Printf("[thumbnails] unable to inspect ffmpeg HTTP options: %v", err)
			return
		}
		m.httpInputOptions = options
	})
	return append([]string(nil), m.httpInputOptions...)
}

func (m *ThumbnailManager) frameInput(key, sourceURL, authHeader string) (string, string, []string) {
	bridgeURL, bridgeErr := m.sourceBridge.register(key, sourceURL, authHeader)
	httpOptions := m.ffmpegHTTPOptions()
	if m.sharedProtocolAvailable() {
		cacheDir := filepath.Join(m.baseDir, "shared-source-cache")
		if bridgeErr == nil {
			if err := os.MkdirAll(cacheDir, 0o755); err == nil {
				return "shared:" + bridgeURL, "", append([]string{"-cache_dir", cacheDir}, httpOptions...)
			}
		}
	}
	if bridgeErr == nil {
		return bridgeURL, "", httpOptions
	}
	log.Printf("[thumbnails] source bridge unavailable key=%s: %v", key, bridgeErr)
	return sourceURL, authHeader, nil
}

func (m *ThumbnailManager) prewarm(cleanPath, sourceURL, authHeader string) {
	if m == nil || strings.TrimSpace(sourceURL) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), thumbnailSourcePrewarmTimeout)
	defer cancel()
	key := thumbnailKey(cleanPath)
	m.prewarmMu.Lock()
	if last := m.prewarmed[key]; !last.IsZero() && time.Since(last) < thumbnailSourcePrewarmTTL {
		m.prewarmMu.Unlock()
		return
	}
	m.prewarmed[key] = time.Now()
	m.prewarmMu.Unlock()
	if err := m.sourceBridge.prewarm(ctx, key, sourceURL, authHeader); err != nil {
		m.prewarmMu.Lock()
		delete(m.prewarmed, key)
		m.prewarmMu.Unlock()
		log.Printf("[thumbnails] source prewarm failed key=%s path=%q: %v", key, cleanPath, err)
		return
	}
	log.Printf("[thumbnails] source prewarmed key=%s path=%q", key, cleanPath)
}
