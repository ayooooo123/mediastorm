package jellyfinbridge

import (
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"novastream/config"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func bridgeUp(port int) bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/System/Info/Public", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// The bridge only listens while enabled; listener changes restart it.
func TestControllerStartsAndStops(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	deps := h.srv.deps
	deps.CacheDir = t.TempDir()
	c := NewController()
	if err := c.Apply(config.JellyfinBridgeSettings{Enabled: true}); err == nil {
		t.Fatal("an unconfigured controller started")
	}
	c.Configure(deps, func() (config.JellyfinBridgeSettings, error) {
		return config.JellyfinBridgeSettings{ServerID: "0123456789abcdef0123456789abcdef"}, nil
	})
	defer c.Shutdown(t.Context())

	port := freePort(t)
	cfg := config.JellyfinBridgeSettings{Host: "127.0.0.1", Port: port}
	if err := c.Apply(cfg); err != nil || c.Status().Running || bridgeUp(port) {
		t.Fatalf("disabled bridge runs: %v %+v", err, c.Status())
	}
	cfg.Enabled = true
	if err := c.Apply(cfg); err != nil || !c.Status().Running || !bridgeUp(port) {
		t.Fatalf("enabled bridge not running: %v %+v", err, c.Status())
	}
	if c.Status().ServerID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("server id = %q", c.Status().ServerID)
	}

	// A live setting keeps the listener; a new port moves it.
	cfg.QuickConnect = true
	if err := c.Apply(cfg); err != nil || !bridgeUp(port) {
		t.Fatalf("live setting change broke the bridge: %v", err)
	}
	moved := freePort(t)
	cfg.Port = moved
	if err := c.Apply(cfg); err != nil || c.Status().Port != moved || !bridgeUp(moved) || bridgeUp(port) {
		t.Fatalf("port change: %v %+v", err, c.Status())
	}

	cfg.Enabled = false
	if err := c.Apply(cfg); err != nil || c.Status().Running || bridgeUp(moved) {
		t.Fatalf("disabled bridge still listening: %v %+v", err, c.Status())
	}
}

// A port that is taken is reported, and a later Apply can still start.
func TestControllerReportsListenError(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	deps := h.srv.deps
	deps.CacheDir = t.TempDir()
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	c := NewController()
	c.Configure(deps, nil)
	defer c.Shutdown(t.Context())
	cfg := config.JellyfinBridgeSettings{Enabled: true, Host: "127.0.0.1", Port: busy.Addr().(*net.TCPAddr).Port}
	if err := c.Apply(cfg); err == nil || c.Status().Running || c.Status().Error == "" {
		t.Fatalf("taken port: %v %+v", err, c.Status())
	}
	cfg.Port = freePort(t)
	if err := c.Apply(cfg); err != nil || !c.Status().Running || c.Status().Error != "" {
		t.Fatalf("retry: %v %+v", err, c.Status())
	}
}
