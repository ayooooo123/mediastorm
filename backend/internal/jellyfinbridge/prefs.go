package jellyfinbridge

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// prefsStore persists Jellyfin display preferences (client UI settings such as
// home-screen sections, sort orders and the web client's "usersettings").
// They are opaque to the bridge: stored and returned verbatim per profile.
type prefsStore struct {
	mu    sync.Mutex
	path  string
	data  map[string]json.RawMessage
	dirty bool
	stop  chan struct{}
	done  chan struct{}
}

func newPrefsStore(path string) *prefsStore {
	p := &prefsStore{path: path, data: map[string]json.RawMessage{}, stop: make(chan struct{}), done: make(chan struct{})}
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(raw, &p.data); err != nil {
				log.Printf("[jellyfin-bridge] display prefs load failed: %v", err)
				p.data = map[string]json.RawMessage{}
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("[jellyfin-bridge] display prefs read failed: %v", err)
		}
	}
	go p.loop()
	return p
}

func prefsKey(profileID, prefsID, client string) string {
	return profileID + "|" + prefsID + "|" + client
}

func (p *prefsStore) get(key string) (json.RawMessage, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.data[key]
	return v, ok
}

// maxPrefsPerProfile bounds stored preference documents; clients use a
// handful (usersettings, home sections, per-library views).
const maxPrefsPerProfile = 200

func (p *prefsStore) set(key string, value json.RawMessage) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.data[key]; !exists {
		profile, _, _ := strings.Cut(key, "|")
		count := 0
		for k := range p.data {
			if strings.HasPrefix(k, profile+"|") {
				count++
			}
		}
		if count >= maxPrefsPerProfile {
			return false
		}
	}
	p.data[key] = value
	p.dirty = true
	return true
}

func (p *prefsStore) flush() {
	if p.path == "" {
		return
	}
	p.mu.Lock()
	if !p.dirty {
		p.mu.Unlock()
		return
	}
	raw, err := json.Marshal(p.data)
	p.dirty = false
	p.mu.Unlock()
	if err == nil {
		err = os.MkdirAll(filepath.Dir(p.path), 0o755)
	}
	if err == nil {
		tmp := p.path + ".tmp"
		if err = os.WriteFile(tmp, raw, 0o600); err == nil {
			err = os.Rename(tmp, p.path)
		}
	}
	if err != nil {
		log.Printf("[jellyfin-bridge] display prefs write: %v", err)
		p.mu.Lock()
		p.dirty = true // retry on the next tick
		p.mu.Unlock()
	}
}

func (p *prefsStore) loop() {
	defer close(p.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.flush()
		case <-p.stop:
			p.flush()
			return
		}
	}
}

func (p *prefsStore) close() {
	select {
	case <-p.stop:
		return
	default:
		close(p.stop)
	}
	<-p.done
}

func defaultDisplayPrefs(id, client string) map[string]any {
	return map[string]any{
		"Id":                 id,
		"SortBy":             "SortName",
		"RememberIndexing":   false,
		"PrimaryImageHeight": 250,
		"PrimaryImageWidth":  250,
		"CustomPrefs":        map[string]string{},
		"ScrollDirection":    "Horizontal",
		"ShowBackdrop":       true,
		"RememberSorting":    false,
		"SortOrder":          "Ascending",
		"ShowSidebar":        false,
		"Client":             client,
	}
}

func (s *Server) handleGetDisplayPrefs(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	id := pathVar(r, "id")
	client := queryValue(r, "client")
	if stored, ok := s.prefs.get(prefsKey(p.Profile.ID, id, client)); ok {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(stored)
		return
	}
	writeJSON(w, http.StatusOK, defaultDisplayPrefs(id, client))
}

func (s *Server) handleSetDisplayPrefs(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256*1024))
	if err != nil || !json.Valid(body) {
		writeError(w, http.StatusBadRequest, "Invalid display preferences")
		return
	}
	if !s.prefs.set(prefsKey(p.Profile.ID, pathVar(r, "id"), queryValue(r, "client")), body) {
		writeError(w, http.StatusBadRequest, "Too many display preference documents")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
