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

func TestStreamMediaMetadataLiveAliasesPopulateDashboardFields(t *testing.T) {
	req := httptest.NewRequest("GET", "/live/hls/start?mediaType=channel&url=https%3A%2F%2Fiptv.example%2Fnews.m3u8&sourceId=provider-1&channelLogo=https%3A%2F%2Fimages.example%2Fnews.png", nil)
	meta := parseStreamMediaMetadata(req)

	if meta.LiveSourceURL != "https://iptv.example/news.m3u8" {
		t.Fatalf("LiveSourceURL = %q", meta.LiveSourceURL)
	}
	if meta.LiveSourceID != "provider-1" {
		t.Fatalf("LiveSourceID = %q", meta.LiveSourceID)
	}
	if meta.LiveChannelLogo != "https://images.example/news.png" {
		t.Fatalf("LiveChannelLogo = %q", meta.LiveChannelLogo)
	}
}

func TestStreamMediaMetadataLiveExplicitFieldsWinOverAliases(t *testing.T) {
	req := httptest.NewRequest("GET", "/live/hls/start?mediaType=live&url=https%3A%2F%2Fold.example%2Flive&sourceId=old&channelLogo=https%3A%2F%2Fold.example%2Flogo.png&liveSourceUrl=https%3A%2F%2Fnew.example%2Flive&liveSourceId=new&liveChannelLogo=https%3A%2F%2Fnew.example%2Flogo.png", nil)
	meta := parseStreamMediaMetadata(req)

	if meta.LiveSourceURL != "https://new.example/live" || meta.LiveSourceID != "new" || meta.LiveChannelLogo != "https://new.example/logo.png" {
		t.Fatalf("explicit live metadata should win: %+v", meta)
	}
}
