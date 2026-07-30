package debrid

import (
	"testing"
	"time"
)

func TestZileanParseResponsePreservesIngestedAt(t *testing.T) {
	scraper := NewZileanScraper("https://zilean.test", "Zilean", nil)
	results, err := scraper.parseResponse([]byte(`[
		{
			"raw_title": "Movie.2026.1080p.WEB-DL",
			"size": "123456789",
			"info_hash": "0123456789abcdef0123456789abcdef01234567",
			"resolution": "1080p",
			"languages": ["en"],
			"ingested_at": "2026-07-30T18:34:00.123456Z"
		}
	]`))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	want := time.Date(2026, time.July, 30, 18, 34, 0, 123456000, time.UTC)
	if !results[0].PublishDate.Equal(want) {
		t.Fatalf("PublishDate = %v, want %v", results[0].PublishDate, want)
	}
}

func TestZileanParseResponseIgnoresInvalidIngestedAt(t *testing.T) {
	scraper := NewZileanScraper("https://zilean.test", "Zilean", nil)
	results, err := scraper.parseResponse([]byte(`[
		{
			"raw_title": "Movie.2026.1080p.WEB-DL",
			"size": 123456789,
			"info_hash": "0123456789abcdef0123456789abcdef01234567",
			"ingested_at": "not-a-timestamp"
		}
	]`))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if !results[0].PublishDate.IsZero() {
		t.Fatalf("PublishDate = %v, want zero time", results[0].PublishDate)
	}
}
