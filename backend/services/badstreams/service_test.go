package badstreams

import (
	"path/filepath"
	"testing"

	"novastream/models"
)

func TestServiceMatchesProviderSpecificDebridStream(t *testing.T) {
	svc := New(filepath.Join(t.TempDir(), "bad_streams.json"))
	_, err := svc.Mark(MarkRequest{
		ReleaseName: "Movie.2024.2160p.WEB-DL-GROUP",
		ServiceType: "debrid",
		Provider:    "real-debrid",
		Reason:      "migration",
	})
	if err != nil {
		t.Fatalf("Mark returned error: %v", err)
	}

	realDebrid := models.NZBResult{
		Title:       "Movie 2024 2160p WEB DL GROUP",
		ServiceType: models.ServiceTypeDebrid,
		Attributes:  map[string]string{"provider": "realdebrid"},
	}
	if !svc.IsBad(realDebrid) {
		t.Fatal("expected matching Real-Debrid release to be bad")
	}

	allDebrid := realDebrid
	allDebrid.Attributes = map[string]string{"provider": "alldebrid"}
	if svc.IsBad(allDebrid) {
		t.Fatal("provider-specific mark should not block a different debrid provider")
	}
}

func TestServiceProviderWideMarkMatchesAnyDebridProvider(t *testing.T) {
	svc := New(filepath.Join(t.TempDir(), "bad_streams.json"))
	_, err := svc.Mark(MarkRequest{
		ReleaseName: "Show.S01E02.1080p.WEB-DL-GROUP",
		ServiceType: "debrid",
		Reason:      "manual",
	})
	if err != nil {
		t.Fatalf("Mark returned error: %v", err)
	}

	candidate := models.NZBResult{
		Title:       "Show S01E02 1080p WEB DL GROUP",
		ServiceType: models.ServiceTypeDebrid,
		Attributes:  map[string]string{"provider": "alldebrid"},
	}
	if !svc.IsBad(candidate) {
		t.Fatal("expected provider-wide debrid mark to match candidate")
	}
}

func TestServiceMarkInfersDebridProviderFromSourcePath(t *testing.T) {
	svc := New(filepath.Join(t.TempDir(), "bad_streams.json"))
	_, err := svc.Mark(MarkRequest{
		ReleaseName: "Movie.2024.2160p.WEB-DL-GROUP",
		ServiceType: "debrid",
		SourcePath:  "/debrid/realdebrid/torrent-1/file/0/Movie.mkv",
	})
	if err != nil {
		t.Fatalf("Mark returned error: %v", err)
	}

	realDebrid := models.NZBResult{
		Title:       "Movie.2024.2160p.WEB-DL-GROUP",
		ServiceType: models.ServiceTypeDebrid,
		Attributes:  map[string]string{"provider": "real-debrid"},
	}
	if !svc.IsBad(realDebrid) {
		t.Fatal("expected source path provider to match Real-Debrid candidate")
	}

	allDebrid := realDebrid
	allDebrid.Attributes = map[string]string{"provider": "alldebrid"}
	if svc.IsBad(allDebrid) {
		t.Fatal("source path provider should not block a different debrid provider")
	}
}

func TestServiceFilterResultsKeepsUnmarkedOrder(t *testing.T) {
	svc := New(filepath.Join(t.TempDir(), "bad_streams.json"))
	_, err := svc.Mark(MarkRequest{
		ReleaseName: "Bad.Release",
		ServiceType: "usenet",
	})
	if err != nil {
		t.Fatalf("Mark returned error: %v", err)
	}

	results := []models.NZBResult{
		{Title: "Good.Release.1", ServiceType: models.ServiceTypeUsenet},
		{Title: "Bad Release", ServiceType: models.ServiceTypeUsenet},
		{Title: "Good.Release.2", ServiceType: models.ServiceTypeDebrid, Attributes: map[string]string{"provider": "realdebrid"}},
	}

	filtered := svc.FilterResults(results)
	if len(filtered) != 2 {
		t.Fatalf("filtered length = %d, want 2", len(filtered))
	}
	if filtered[0].Title != "Good.Release.1" || filtered[1].Title != "Good.Release.2" {
		t.Fatalf("unexpected filtered order: %#v", filtered)
	}
}
