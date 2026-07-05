package handlers

import "testing"

func TestFilterSubtitleResultsRejectsUnrelatedTitle(t *testing.T) {
	year := 2026
	results := []SubtitleResult{
		{ID: "1", Provider: "test", Release: "Obsession.2026.2160p.WEB-DL.H265-GROUP", Downloads: 10},
		{ID: "2", Provider: "test", Release: "WhatsApp.Obsession.The.Murder.of.Stephanie.Hansen.2026.1080p.WEB-DL", Downloads: 1000},
	}

	filtered := filterSubtitleResults(results, SubtitleSearchParams{
		Title: "Obsession",
		Year:  &year,
	})

	if len(filtered) != 1 {
		t.Fatalf("expected 1 subtitle result, got %d: %+v", len(filtered), filtered)
	}
	if filtered[0].ID != "1" {
		t.Fatalf("expected relevant subtitle to remain, got %+v", filtered[0])
	}
}

func TestFilterSubtitleResultsRejectsWrongEpisode(t *testing.T) {
	year := 2020
	season := 1
	episode := 1
	results := []SubtitleResult{
		{ID: "1", Provider: "test", Release: "The.Owl.House.S01E01.1080p.WEBRip", Downloads: 10},
		{ID: "2", Provider: "test", Release: "The.Owl.House.S01E19.1080p.WEBRip", Downloads: 1000},
	}

	filtered := filterSubtitleResults(results, SubtitleSearchParams{
		Title:   "The Owl House",
		Year:    &year,
		Season:  &season,
		Episode: &episode,
	})

	if len(filtered) != 1 {
		t.Fatalf("expected 1 subtitle result, got %d: %+v", len(filtered), filtered)
	}
	if filtered[0].ID != "1" {
		t.Fatalf("expected matching episode subtitle to remain, got %+v", filtered[0])
	}
}
