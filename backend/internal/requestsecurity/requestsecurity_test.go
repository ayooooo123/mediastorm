package requestsecurity

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestClientIPIgnoresForwardedHeadersFromUntrustedPeer(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.test", nil)
	req.RemoteAddr = "198.51.100.10:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got := ClientIP(req); got != "198.51.100.10" {
		t.Fatalf("ClientIP() = %q, want TCP peer", got)
	}
}

func TestClientIPTrustsLoopbackProxy(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.test", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got := ClientIP(req); got != "203.0.113.7" {
		t.Fatalf("ClientIP() = %q, want forwarded address", got)
	}
}

func TestIsLoopbackUsesRemoteAddressNotHostHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "http://localhost", nil)
	req.RemoteAddr = "198.51.100.10:1234"
	if IsLoopback(req) {
		t.Fatal("spoofed Host header must not be treated as loopback")
	}
}

func TestValidateOutboundURLRejectsRestrictedDestinations(t *testing.T) {
	for _, rawURL := range []string{
		"http://127.0.0.1:7777/api/settings",
		"http://[::1]/health",
		"http://169.254.169.254/latest/meta-data/",
		"file:///etc/passwd",
	} {
		if err := ValidateOutboundURL(context.Background(), rawURL, nil); err == nil {
			t.Errorf("ValidateOutboundURL(%q) error = nil, want rejection", rawURL)
		}
	}
}

func TestValidateOutboundURLAllowsConfiguredRestrictedHost(t *testing.T) {
	policy := func(hostname string) bool { return hostname == "127.0.0.1" }
	if err := ValidateOutboundURL(context.Background(), "http://127.0.0.1:8080/media", policy); err != nil {
		t.Fatalf("configured provider URL rejected: %v", err)
	}
}

func TestValidateOutboundURLAllowsPublicLiteral(t *testing.T) {
	if err := ValidateOutboundURL(context.Background(), "https://192.0.2.1/media", nil); err != nil {
		t.Fatalf("public URL rejected: %v", err)
	}
}

func TestURLForLogRemovesCredentialsAndResourceData(t *testing.T) {
	got := URLForLog("https://user:password@example.com:8443/private/token/file.mkv?signature=secret#fragment")
	if got != "https://example.com:8443" {
		t.Fatalf("URLForLog() = %q", got)
	}
}
