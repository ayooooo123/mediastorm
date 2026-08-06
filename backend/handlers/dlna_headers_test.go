package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseContentRangeTotal(t *testing.T) {
	cases := []struct {
		header string
		want   int64
	}{
		{"bytes 0-100/18339538491", 18339538491},
		{"bytes 18337932262-18339538490/18339538491", 18339538491},
		{"bytes 0-100/*", 0},
		{"", 0},
		{"bytes 0-100", 0},
		{"bytes 0-100/notanumber", 0},
		{"bytes 0-100/0", 0},
	}
	for _, tc := range cases {
		if got := parseContentRangeTotal(tc.header); got != tc.want {
			t.Errorf("parseContentRangeTotal(%q) = %d, want %d", tc.header, got, tc.want)
		}
	}
}

func TestParseContentRangeStart(t *testing.T) {
	cases := []struct {
		header string
		want   int64
		ok     bool
	}{
		{"bytes 0-100/18339538491", 0, true},
		{"bytes 2097152-4194303/18339538491", 2097152, true},
		{"bytes 2097152-4194303/*", 2097152, true},
		{"", 0, false},
		{"bytes /100", 0, false},
		{"bytes -100/200", 0, false},
		{"bytes abc-100/200", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseContentRangeStart(tc.header)
		if ok != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("parseContentRangeStart(%q) = (%d,%v), want (%d,%v)", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}

// A 206 that reports "/*" leaves the renderer unable to determine file size,
// which is what makes LG webOS abort playback. Once the total is known every
// partial response must name it.
func TestRangeCacheContentRangeReportsRealTotal(t *testing.T) {
	var c rangeBlockCache

	if got, want := c.contentRangeValue("/movie.mkv", 0, 100), "bytes 0-100/*"; got != want {
		t.Fatalf("before total known: got %q, want %q", got, want)
	}

	c.setTotal("/movie.mkv", 18339538491)

	if got, want := c.contentRangeValue("/movie.mkv", 0, 100), "bytes 0-100/18339538491"; got != want {
		t.Fatalf("after total known: got %q, want %q", got, want)
	}
	// A different file must not inherit the total.
	if got, want := c.contentRangeValue("/other.mkv", 0, 100), "bytes 0-100/*"; got != want {
		t.Fatalf("unrelated path: got %q, want %q", got, want)
	}
}

func TestRangeCacheTotalIgnoresUnknownAndDoesNotRegress(t *testing.T) {
	var c rangeBlockCache

	c.setTotal("/movie.mkv", 0)
	if got := c.total("/movie.mkv"); got != 0 {
		t.Fatalf("zero total should not be recorded, got %d", got)
	}
	c.setTotal("/movie.mkv", -5)
	if got := c.total("/movie.mkv"); got != 0 {
		t.Fatalf("negative total should not be recorded, got %d", got)
	}

	c.setTotal("/movie.mkv", 18339538491)
	// A later partial-window response must not shrink the known file length.
	c.setTotal("/movie.mkv", 4096)
	if got, want := c.total("/movie.mkv"), int64(18339538491); got != want {
		t.Fatalf("total regressed: got %d, want %d", got, want)
	}
}

func TestWriteDlnaHeadersEchoesTransferMode(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/video/stream", nil)
	req.Header.Set("transferMode.dlna.org", "Interactive")

	writeDlnaHeaders(rec, req)

	// Keys must keep DLNA spec casing, so read the map directly rather than
	// through Get, which canonicalizes.
	if got := rec.Header()["transferMode.dlna.org"]; len(got) != 1 || got[0] != "Interactive" {
		t.Fatalf("transferMode not echoed: %v", got)
	}
	if got := rec.Header()["contentFeatures.dlna.org"]; len(got) != 1 || got[0] != dlnaContentFeatures {
		t.Fatalf("contentFeatures missing: %v", got)
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", got)
	}
}

func TestWriteDlnaHeadersDefaultsToStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/video/stream", nil)

	writeDlnaHeaders(rec, req)

	if got := rec.Header()["transferMode.dlna.org"]; len(got) != 1 || got[0] != "Streaming" {
		t.Fatalf("transferMode default: %v", got)
	}
}

// The DIDL-Lite the app sends advertises DLNA.ORG_OP=01; the HTTP response has
// to agree or a strict renderer treats the resource as inconsistent.
func TestDlnaContentFeaturesAdvertisesByteSeek(t *testing.T) {
	const want = "DLNA.ORG_OP=01;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000"
	if dlnaContentFeatures != want {
		t.Fatalf("contentFeatures = %q, want %q", dlnaContentFeatures, want)
	}
}
