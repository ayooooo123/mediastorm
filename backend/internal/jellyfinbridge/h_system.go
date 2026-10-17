package jellyfinbridge

import (
	"net"
	"net/http"
	"runtime"
	"strings"

	"novastream/internal/jellyfinbridge/dto"
)

func (s *Server) publicSystemInfo(r *http.Request) dto.PublicSystemInfo {
	return dto.PublicSystemInfo{
		LocalAddress:           requestBaseURL(r),
		ServerName:             s.settings().ServerName,
		Version:                ServerVersion,
		ProductName:            productName,
		OperatingSystem:        "",
		ID:                     s.serverID,
		StartupWizardCompleted: true,
	}
}

func (s *Server) handlePublicSystemInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.publicSystemInfo(r))
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	info := dto.SystemInfo{
		PublicSystemInfo:           s.publicSystemInfo(r),
		OperatingSystemDisplayName: runtime.GOOS,
		PackageName:                "mediastorm",
		SupportsLibraryMonitor:     false,
		WebSocketPortNumber:        s.cfg.Port,
		CompletedInstallations:     []any{},
		CastReceiverApplications:   []any{},
		EncoderLocation:            "System",
		SystemArchitecture:         runtime.GOARCH,
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handlePing(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`"Jellyfin Server"`))
}

func (s *Server) handleEndpointInfo(w http.ResponseWriter, r *http.Request) {
	parsed := net.ParseIP(s.clientIP(r))
	local := parsed != nil && (parsed.IsPrivate() || parsed.IsLoopback() || parsed.IsLinkLocalUnicast())
	writeJSON(w, http.StatusOK, map[string]bool{"IsLocal": local, "IsInNetwork": local})
}

func (s *Server) handleBrandingConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, dto.BrandingOptions{SplashscreenEnabled: false})
}

func (s *Server) handleBrandingCSS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleBitrateTest(w http.ResponseWriter, r *http.Request) {
	size := parseIntDefault(queryValue(r, "size"), 500000)
	if size <= 0 || size > 10_000_000 {
		size = 500000
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", itoa(size))
	buf := make([]byte, 64*1024)
	for remaining := size; remaining > 0; {
		n := len(buf)
		if remaining < n {
			n = remaining
		}
		if _, err := w.Write(buf[:n]); err != nil {
			return
		}
		remaining -= n
	}
}

// requestBaseURL reconstructs the externally visible base URL of the bridge.
func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); proto != "" {
		scheme = proto
	}
	host := r.Host
	if fwd := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); fwd != "" {
		host = fwd
	}
	return scheme + "://" + host
}
