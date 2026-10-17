package jellyfinbridge

import (
	"context"
	"errors"
	"log"
	"slices"
	"sync"
	"time"

	"novastream/config"
)

// Controller runs the bridge while it is enabled: the admin Integrations page
// turns it on and off, and listener settings (host, port, discovery, trusted
// proxies) restart it, without restarting mediastorm.
type Controller struct {
	mu       sync.Mutex
	deps     *Deps
	ensureID func() (config.JellyfinBridgeSettings, error)
	server   *Server
	running  config.JellyfinBridgeSettings // settings the running server was started with
	lastErr  error
}

// ControllerStatus describes the bridge for the admin page.
type ControllerStatus struct {
	Running  bool   `json:"running"`
	Port     int    `json:"port,omitempty"`
	ServerID string `json:"serverId,omitempty"`
	Error    string `json:"error,omitempty"`
}

// NewController returns a controller that starts nothing until Configure.
func NewController() *Controller { return &Controller{} }

// Configure hands the controller mediastorm's services. ensureID returns the
// bridge settings with a persisted server ID.
func (c *Controller) Configure(deps Deps, ensureID func() (config.JellyfinBridgeSettings, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deps, c.ensureID = &deps, ensureID
}

// Apply starts, stops or restarts the bridge for cfg.
func (c *Controller) Apply(cfg config.JellyfinBridgeSettings) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.deps == nil {
		return errors.New("jellyfin bridge is not configured yet")
	}
	if c.server != nil && (!cfg.Enabled || listenerChanged(c.running, cfg)) {
		c.stopLocked()
	}
	if !cfg.Enabled || c.server != nil {
		c.lastErr = nil
		return nil
	}
	if c.ensureID != nil {
		if withID, err := c.ensureID(); err != nil {
			log.Printf("[jellyfin-bridge] could not persist server id: %v", err)
		} else {
			cfg.ServerID = withID.ServerID
		}
	}
	srv := New(*c.deps, cfg)
	if err := srv.Start(); err != nil {
		c.lastErr = err
		log.Printf("[jellyfin-bridge] not started: %v", err)
		return err
	}
	c.server, c.running, c.lastErr = srv, cfg, nil
	log.Printf("[jellyfin-bridge] started on port %d", srv.Port())
	return nil
}

// listenerChanged reports whether a settings change needs a new listener;
// everything else is read live.
func listenerChanged(old, cfg config.JellyfinBridgeSettings) bool {
	return old.Host != cfg.Host || old.Port != cfg.Port || old.UDPDiscovery != cfg.UDPDiscovery ||
		!slices.Equal(old.TrustedProxies, cfg.TrustedProxies)
}

func (c *Controller) stopLocked() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.server.Shutdown(ctx); err != nil {
		log.Printf("[jellyfin-bridge] shutdown: %v", err)
	}
	log.Printf("[jellyfin-bridge] stopped")
	c.server = nil
}

// Status reports whether the bridge runs and why it does not.
func (c *Controller) Status() ControllerStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := ControllerStatus{}
	if c.server != nil {
		st.Running, st.Port, st.ServerID = true, c.server.Port(), c.running.ServerID
	}
	if c.lastErr != nil {
		st.Error = c.lastErr.Error()
	}
	return st
}

// Shutdown stops the bridge (mediastorm shutting down).
func (c *Controller) Shutdown(context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.server != nil {
		c.stopLocked()
	}
}
