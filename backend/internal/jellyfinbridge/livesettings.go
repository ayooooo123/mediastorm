package jellyfinbridge

import (
	"strings"
	"sync"
	"time"

	"novastream/config"
)

// liveSettingsTTL bounds how often the settings file is re-read.
const liveSettingsTTL = 2 * time.Second

type liveSettings struct {
	mu     sync.Mutex
	cfg    config.JellyfinBridgeSettings
	loaded time.Time
}

// settings returns the bridge settings as currently saved. Behaviour toggles
// (copying, transcoding, Quick Connect, sign-in options, waits) follow the
// admin UI immediately; the listener keeps the address, port and server ID
// it started with (s.cfg).
func (s *Server) settings() config.JellyfinBridgeSettings {
	if s.deps.Settings == nil {
		return s.cfg
	}
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	if !s.live.loaded.IsZero() && time.Since(s.live.loaded) < liveSettingsTTL {
		return s.live.cfg
	}
	cfg, err := s.deps.Settings()
	if err != nil {
		if s.live.loaded.IsZero() {
			return s.cfg
		}
		return s.live.cfg
	}
	cfg.Host, cfg.Port, cfg.ServerID, cfg.UDPDiscovery, cfg.TrustedProxies = s.cfg.Host, s.cfg.Port, s.cfg.ServerID, s.cfg.UDPDiscovery, s.cfg.TrustedProxies
	if strings.TrimSpace(cfg.ServerName) == "" {
		cfg.ServerName = s.cfg.ServerName
	}
	if cfg.PlaybackInfoWaitSeconds <= 0 {
		cfg.PlaybackInfoWaitSeconds = s.cfg.PlaybackInfoWaitSeconds
	}
	s.live.cfg, s.live.loaded = cfg, time.Now()
	return cfg
}
