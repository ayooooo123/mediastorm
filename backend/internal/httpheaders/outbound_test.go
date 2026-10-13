package httpheaders

import (
	"net/http"
	"testing"
)

func TestSetHeadersUseOverrideWhenProvided(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid", nil)

	SetNZBDownloadHeaders(req, "  ")
	if got := req.Header.Get("User-Agent"); got != NZBDownloadUserAgent {
		t.Fatalf("blank override: User-Agent = %q, want %q", got, NZBDownloadUserAgent)
	}
	SetNZBDownloadHeaders(req, "Radarr/5.0")
	if got := req.Header.Get("User-Agent"); got != "Radarr/5.0" {
		t.Fatalf("NZB override: User-Agent = %q", got)
	}

	SetIndexerSearchHeaders(req, "")
	if got := req.Header.Get("User-Agent"); got != UserAgent {
		t.Fatalf("search default: User-Agent = %q, want %q", got, UserAgent)
	}
	SetIndexerSearchHeaders(req, "Prowlarr/2.0")
	if got := req.Header.Get("User-Agent"); got != "Prowlarr/2.0" {
		t.Fatalf("search override: User-Agent = %q", got)
	}
}
