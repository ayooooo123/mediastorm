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

// SourceRoute is where the relay pulls bytes through MediaStorm's own egress,
// headers, debrid unrestriction, and Usenet readers.
const SourceRoute = "/internal/peartube/source"

// Idle tokens stay reusable across Range reconnects until this TTL expires; active streams pin them open.
const sourceTokenTTL = 24 * time.Hour

// Sources hands out bearer tokens scoped to one stream path for sourceTokenTTL.
// The token rides in `Authorization: Bearer <token>` inside the relay's private
// jobs.json, never in the URL or Corestore.
type Sources struct {
	provider streaming.Provider
	external func(http.ResponseWriter, *http.Request, string)

	mu      sync.Mutex
	pending map[string]pendingSource
}

type pendingSource struct {
	path    string
	expires time.Time
	active  int
}

// NewSources serves stream paths through provider.
func NewSources(provider streaming.Provider) *Sources {
	return &Sources{provider: provider, pending: make(map[string]pendingSource)}
}

// SetExternalProxy wires VideoHandler's external HTTP proxy so addon/WebDAV URLs
// inherit streamheaders, VLC User-Agent, WebDAV auth, and redirect refresh.
func (s *Sources) SetExternalProxy(fn func(http.ResponseWriter, *http.Request, string)) {
	s.external = fn
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
		if entry.active == 0 && now.After(entry.expires) {
			delete(s.pending, key)
		}
	}
	s.pending[token] = pendingSource{path: path, expires: now.Add(sourceTokenTTL)}
	return token, nil
}

// acquire pins token open while bytes flow and slides its idle TTL so multi-hour 4K remux Range retries never 401.
func (s *Sources) acquire(token string) (string, bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.pending[token]
	if !ok || (entry.active == 0 && now.After(entry.expires)) {
		delete(s.pending, token)
		return "", false
	}
	entry.active++
	s.pending[token] = entry
	return entry.path, true
}

func (s *Sources) release(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.pending[token]
	entry.active--
	entry.expires = time.Now().Add(sourceTokenTTL)
	s.pending[token] = entry
}

// ServeHTTP streams the path a valid token names, honouring Range reconnects.
func (s *Sources) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found || token == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path, ok := s.acquire(token)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	defer s.release(token)
	if (strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")) && s.external != nil {
		s.external(w, r, path)
		return
	}
	method := r.Method
	if method != http.MethodHead {
		method = http.MethodGet
	}
	resp, err := s.provider.Stream(r.Context(), streaming.Request{
		Path:        path,
		Method:      method,
		RangeHeader: r.Header.Get("Range"),
		IfRange:     r.Header.Get("If-Range"),
	})
	if err != nil {
		log.Printf("[peartube] source %q unavailable: %v", path, err)
		http.Error(w, "source unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Close()
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
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
	// Spit 200/206 + Content-Range onto the wire right now so slow Usenet skipReader seeks never starve the relay's header timer.
	_ = http.NewResponseController(w).Flush()
	if method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("[peartube] source %q copy ended: %v", path, err)
	}
}
