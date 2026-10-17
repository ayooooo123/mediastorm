package jellyfinbridge

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// defaultWebClientDir is where the bridge looks for a jellyfin-web build
// when none is configured. mediastorm does not ship one (jellyfin-web is
// GPL-licensed); admins mount a build there (see the Integrations page).
// Jellyfin's Android app and Jellyfin Media Player are shells around the
// server's own web client (/web), so a bridge without one is unusable for
// them.
const defaultWebClientDir = "/app/jellyfin-web"

// webClientDir returns the jellyfin-web directory to serve, or "".
func (s *Server) webClientDir() string {
	dir := strings.TrimSpace(s.settings().WebClientDir)
	if dir == "" {
		dir = defaultWebClientDir
	}
	if st, err := os.Stat(filepath.Join(dir, "index.html")); err != nil || st.IsDir() {
		return ""
	}
	return dir
}

// webClient serves /web/* from the jellyfin-web build with the request's
// original casing (asset names are case-sensitive, unlike the API), and
// redirects / to /web/ like Jellyfin. Everything else goes to the API.
func (s *Server) webClient(api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lower := strings.ToLower(r.URL.Path)
		isWeb := lower == "/web" || strings.HasPrefix(lower, "/web/")
		if (!isWeb && lower != "/") || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			api.ServeHTTP(w, r)
			return
		}
		dir := s.webClientDir()
		if dir == "" {
			api.ServeHTTP(w, r)
			return
		}
		if !isWeb || lower == "/web" {
			http.Redirect(w, r, "/web/", http.StatusFound)
			return
		}
		rel := path.Clean("/" + r.URL.Path[len("/web/"):])
		if rel == "/" {
			rel = "/index.html"
		}
		file := filepath.Join(dir, filepath.FromSlash(rel))
		st, err := os.Stat(file)
		if err != nil || st.IsDir() {
			if path.Ext(rel) != "" {
				http.NotFound(w, r)
				return
			}
			// Client-side routes fall back to the app.
			file = filepath.Join(dir, "index.html")
		}
		f, err := os.Open(file)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err = f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(file, ".html") || strings.HasSuffix(file, ".json") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		// ServeContent, not ServeFile: ServeFile redirects .../index.html.
		http.ServeContent(w, r, filepath.Base(file), st.ModTime(), f)
	})
}
