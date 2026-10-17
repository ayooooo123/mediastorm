package jellyfinbridge

import (
	"bufio"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// connectionPaths are the requests a client makes while finding the server
// and signing in. They are logged (with any error response) so a client that
// "cannot connect" leaves a trace; browsing and media requests are not.
var connectionPaths = []string{
	"/system/info", "/system/ping", "/users/public", "/users/authenticate",
	"/quickconnect/", "/branding/", "/startup",
}

func isConnectionPath(path string) bool {
	switch path {
	case "/", "/web", "/web/", "/web/index.html": // the web client entry, not its assets
		return true
	}
	for _, prefix := range connectionPaths {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// accessLog logs connection requests and every error response.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		path := strings.ToLower(r.URL.Path)
		// Rejected sockets are logged by handleSocket already (web clients retry
		// them in a loop before signing in).
		if rec.hijacked || (!isConnectionPath(path) && rec.status < 400) || (path == "/socket" && rec.status == http.StatusUnauthorized) {
			return
		}
		info := requestClientInfo(r)
		client := strings.TrimSpace(info.Client + " " + info.Version)
		if info.Device != "" {
			client += " / " + info.Device
		}
		if client == "" {
			client = r.UserAgent()
		}
		log.Printf("[jellyfin-bridge] %s %s -> %d (%v) from %s, client %q", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond), s.clientIP(r), client)
	})
}

// statusRecorder captures the response status; it keeps Hijack (the
// WebSocket) and Flush (streams) working.
type statusRecorder struct {
	http.ResponseWriter
	status   int
	wrote    bool
	hijacked bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacking not supported")
	}
	r.hijacked = true
	return h.Hijack()
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
