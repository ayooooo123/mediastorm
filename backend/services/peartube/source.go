package peartube

import (
	"crypto/rand"
	"encoding/base64"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/services/streaming"
)

// SourceRoute is where the relay fetches bytes that only MediaStorm can read,
// such as a usenet release MediaStorm streams itself.
const SourceRoute = "/internal/peartube/source"

// A token waits this long for the relay to reach its queued job.
const sourceTokenTTL = 24 * time.Hour

// Sources hands out one-time tokens, each good for a single fetch of one
// stream path. The token travels as `Authorization: Bearer <token>` in the
// acquire source headers, never in the URL.
type Sources struct {
	provider streaming.Provider

	mu      sync.Mutex
	pending map[string]pendingSource
}

type pendingSource struct {
	path   string
	issued time.Time
}

// NewSources serves stream paths through provider.
func NewSources(provider streaming.Provider) *Sources {
	return &Sources{provider: provider, pending: make(map[string]pendingSource)}
}

// Issue returns a fresh token for path.
func (s *Sources) Issue(path string) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, entry := range s.pending {
		if now.Sub(entry.issued) > sourceTokenTTL {
			delete(s.pending, key)
		}
	}
	s.pending[token] = pendingSource{path: path, issued: now}
	return token, nil
}

// take consumes token and returns the path it was issued for.
func (s *Sources) take(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.pending[token]
	delete(s.pending, token)
	if !ok || time.Since(entry.issued) > sourceTokenTTL {
		return "", false
	}
	return entry.path, true
}

// ServeHTTP streams the path a valid token names, once.
func (s *Sources) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found || token == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path, ok := s.take(token)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	resp, err := s.provider.Stream(r.Context(), streaming.Request{
		Path:        path,
		Method:      http.MethodGet,
		RangeHeader: r.Header.Get("Range"),
	})
	if err != nil {
		log.Printf("[peartube] source %q unavailable: %v", path, err)
		http.Error(w, "source unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Close()
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if value := resp.Headers.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	if w.Header().Get("Content-Length") == "" && resp.ContentLength > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("[peartube] source %q copy ended: %v", path, err)
	}
}
