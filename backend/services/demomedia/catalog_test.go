package demomedia

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"novastream/services/streaming"
)

func newTestCatalog(t *testing.T, files ...string) *Catalog {
	t.Helper()
	dir := t.TempDir()
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("0123456789"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return NewCatalog(dir)
}

func TestResultsMatchMoviesAndEpisodesByIMDbID(t *testing.T) {
	c := newTestCatalog(t, "tt0063350.mp4", "tt0055662.S01E02.mp4", "tt0055662.S01E03.mp4", "notes.txt")

	movie := c.Results("tt0063350", "Night of the Living Dead", "movie")
	if len(movie) != 1 || movie[0].Link != "demo:tt0063350.mp4" || movie[0].SizeBytes != 10 {
		t.Fatalf("movie results = %+v", movie)
	}
	episode := c.Results("TT0055662", "The Beverly Hillbillies S01E03", "series")
	if len(episode) != 1 || episode[0].Link != "demo:tt0055662.S01E03.mp4" {
		t.Fatalf("episode results = %+v", episode)
	}
	if got := c.Results("tt0055662", "The Beverly Hillbillies S02E01", "series"); len(got) != 0 {
		t.Fatalf("missing episode should not match: %+v", got)
	}
	if got := c.Results("", "Night of the Living Dead", "movie"); len(got) != 0 {
		t.Fatalf("search without an IMDb ID should not match: %+v", got)
	}
	if got := c.Results("tt9999999", "Anything", "movie"); len(got) != 0 {
		t.Fatalf("unknown title should not match: %+v", got)
	}
}

func TestStreamRefusesPathsOutsideTheCatalog(t *testing.T) {
	c := newTestCatalog(t, "tt0063350.mp4")
	for _, path := range []string{"demo:../tt0063350.mp4", "demo:/etc/passwd", "localmedia:1/x.mp4", "demo:notes.txt"} {
		if _, err := c.Stream(context.Background(), streaming.Request{Path: path}); err != streaming.ErrNotFound {
			t.Errorf("Stream(%q) error = %v, want ErrNotFound", path, err)
		}
	}
	if _, err := c.Resolution("demo:tt0000001.mp4"); err == nil {
		t.Error("Resolution of a missing file should fail")
	}
}

func TestStreamServesRanges(t *testing.T) {
	c := newTestCatalog(t, "tt0063350.mp4")
	cases := []struct {
		header string
		status int
		body   string
	}{
		{"", http.StatusOK, "0123456789"},
		{"bytes=2-4", http.StatusPartialContent, "234"},
		{"bytes=7-", http.StatusPartialContent, "789"},
		{"bytes=-2", http.StatusPartialContent, "89"},
		{"bytes=20-", http.StatusRequestedRangeNotSatisfiable, ""},
	}
	for _, tc := range cases {
		resp, err := c.Stream(context.Background(), streaming.Request{Path: "demo:tt0063350.mp4", RangeHeader: tc.header})
		if err != nil {
			t.Fatalf("%q: %v", tc.header, err)
		}
		var body []byte
		if resp.Body != nil {
			body, _ = io.ReadAll(resp.Body)
			_ = resp.Close()
		}
		if resp.Status != tc.status || string(body) != tc.body {
			t.Errorf("%q: status %d body %q, want %d %q", tc.header, resp.Status, body, tc.status, tc.body)
		}
	}
}
