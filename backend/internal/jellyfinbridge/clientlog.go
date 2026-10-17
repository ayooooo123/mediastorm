package jellyfinbridge

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Client logs: Jellyfin apps upload crash reports and logs to
// POST /ClientLog/Document (the Android TV app does so right after a crash).
// The bridge keeps the latest ones under the cache directory so a client
// crash can be diagnosed.

const (
	maxClientLogBytes = 1 << 20
	maxClientLogs     = 50
)

var unsafeLogName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (s *Server) clientLogDir() string {
	if s.deps.CacheDir == "" {
		return ""
	}
	return filepath.Join(s.deps.CacheDir, "jellyfin_bridge", "client_logs")
}

func (s *Server) handleClientLog(w http.ResponseWriter, r *http.Request) {
	// Anonymous: bound uploads per network so a flood cannot push real
	// crash reports out of the newest-maxClientLogs window.
	if !s.login.attempt("clientlog:" + ipLimitKey(s.clientIP(r))) {
		writeError(w, http.StatusTooManyRequests, "Too many log uploads. Try again later.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxClientLogBytes))
	if err != nil || len(body) == 0 {
		writeError(w, http.StatusBadRequest, "Empty or oversized log")
		return
	}
	info := requestClientInfo(r)
	client := unsafeLogName.ReplaceAllString(strings.TrimSpace(info.Client+"_"+info.Version), "-")
	name := fmt.Sprintf("%s_%s.log", time.Now().UTC().Format("20060102-150405.000"), strings.Trim(client, "-_"))
	firstLine, _, _ := strings.Cut(strings.TrimSpace(string(body)), "\n")
	if len(firstLine) > 160 {
		firstLine = firstLine[:160]
	}
	dir := s.clientLogDir()
	if dir == "" {
		log.Printf("[jellyfin-bridge] client log from %s %s (%d bytes, not stored): %s", info.Client, info.Version, len(body), firstLine)
		writeJSON(w, http.StatusOK, map[string]string{"FileName": name})
		return
	}
	if err := os.MkdirAll(dir, 0o755); err == nil {
		err = os.WriteFile(filepath.Join(dir, name), body, 0o600)
		if err != nil {
			log.Printf("[jellyfin-bridge] store client log: %v", err)
		}
		pruneClientLogs(dir)
	}
	log.Printf("[jellyfin-bridge] client log from %s %s stored as %s (%d bytes): %s", info.Client, info.Version, name, len(body), firstLine)
	writeJSON(w, http.StatusOK, map[string]string{"FileName": name})
}

// pruneClientLogs keeps the newest maxClientLogs files.
func pruneClientLogs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= maxClientLogs {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // names start with a UTC timestamp
	for _, name := range names[:max(0, len(names)-maxClientLogs)] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}
