package handlers

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestStreamMediaMetadataSourceServiceTypeRoundTrip(t *testing.T) {
	req := httptest.NewRequest("GET", "/video/stream?sourceServiceType=Debrid", nil)
	meta := parseStreamMediaMetadata(req)
	if meta.SourceServiceType != "debrid" {
		t.Fatalf("SourceServiceType = %q, want debrid", meta.SourceServiceType)
	}

	values := url.Values{}
	addStreamMediaMetadataParams(values, meta)
	if got := values.Get("sourceServiceType"); got != "debrid" {
		t.Fatalf("sourceServiceType param = %q, want debrid", got)
	}
}

func TestStreamMediaMetadataRejectsUnknownSourceServiceType(t *testing.T) {
	req := httptest.NewRequest("GET", "/video/stream?sourceServiceType=unknown", nil)
	if got := parseStreamMediaMetadata(req).SourceServiceType; got != "" {
		t.Fatalf("SourceServiceType = %q, want empty", got)
	}
}
