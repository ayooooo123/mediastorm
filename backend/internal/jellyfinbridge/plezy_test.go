package jellyfinbridge

import (
	"net/http"
	"net/url"
	"testing"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
)

// Plezy asks PlaybackInfo for the item's own version, keeps only a source
// with that ID, and builds the static stream URL itself: item ID as media
// source, the token as api_key, no PlaySessionId.
func TestPlezyStaticStream(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	installFakePlayback(t, h)
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)

	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{"MediaSourceId": item}))
	if len(info.MediaSources) != 1 || info.MediaSources[0].ID != item {
		t.Fatalf("playback info sources = %+v, want the item's own ID", info.MediaSources)
	}

	q := url.Values{"Static": {"true"}, "api_key": {tok}, "DeviceId": {"plezy"}, "MediaSourceId": {item}}
	if rec := h.do(http.MethodGet, "/Videos/"+item+"/stream?"+q.Encode(), "", nil); rec.Code >= 300 {
		t.Fatalf("static stream = %d %s", rec.Code, rec.Body.String())
	}
	// Without a token the item ID is no capability.
	q.Del("api_key")
	if rec := h.do(http.MethodGet, "/Videos/"+item+"/stream?"+q.Encode(), "", nil); rec.Code < 300 {
		t.Fatal("token-less stream by item ID was served")
	}
}

// An external player (Android TV "use external player") takes the source
// whose ID is the item ID from the episode list and streams without
// PlaybackInfo; the bridge resolves the stream on that request.
func TestExternalPlayerStreamWithoutPlaybackInfo(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	installFakePlayback(t, h)
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)

	got := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+item, tok, nil))
	if len(got.MediaSources) == 0 || got.MediaSources[0].ID != item {
		t.Fatalf("item sources = %+v, want one with the item ID", got.MediaSources)
	}
	q := url.Values{"static": {"true"}, "mediaSourceId": {item}, "api_key": {tok}}
	if rec := h.do(http.MethodGet, "/Videos/"+item+"/stream?"+q.Encode(), "", nil); rec.Code >= 300 {
		t.Fatalf("external stream = %d %s", rec.Code, rec.Body.String())
	}
}

// Android TV direct play: no token, no PlaySessionId; it passes the source ID
// it got (the item ID here) and the source ETag as tag.
func TestAndroidTVDirectPlayByTag(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	installFakePlayback(t, h)
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)
	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{"MediaSourceId": item}))
	src := info.MediaSources[0]
	if src.ID != item || src.ETag == "" || src.ETag == item {
		t.Fatalf("source id %s etag %s", src.ID, src.ETag)
	}
	q := url.Values{"static": {"true"}, "mediaSourceId": {src.ID}, "tag": {src.ETag}}
	if rec := h.do(http.MethodGet, "/Videos/"+item+"/stream.mkv?"+q.Encode(), "", nil); rec.Code >= 300 {
		t.Fatalf("direct play by tag = %d %s", rec.Code, rec.Body.String())
	}
	// A guessed item ID alone is no capability.
	q.Del("tag")
	if rec := h.do(http.MethodGet, "/Videos/"+item+"/stream.mkv?"+q.Encode(), "", nil); rec.Code < 300 {
		t.Fatal("token-less stream by item ID was served")
	}
}
