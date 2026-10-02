package customlists

import (
	"testing"

	"novastream/models"
)

func TestTextPosterSurvivesUpsertAndReload(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc.CreateList("profile", "Favorites")
	if err != nil {
		t.Fatal(err)
	}
	input := models.WatchlistUpsert{
		ID: "tmdb:movie:12092", MediaType: "movie", Name: "Alice in Wonderland",
		PosterURL: "plain.jpg", TextPosterURL: "text.jpg", ExternalIDs: map[string]string{"tmdb": "12092"},
	}
	added, err := svc.AddItem("profile", list.ID, input)
	if err != nil || added.TextPosterURL != "text.jpg" {
		t.Fatalf("text poster lost on add: %+v, %v", added, err)
	}
	// A later payload without artwork must preserve the original text poster.
	input.TextPosterURL = ""
	if _, err := svc.AddItem("profile", list.ID, input); err != nil {
		t.Fatal(err)
	}
	svc, err = NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	items, err := svc.ListItems("profile", list.ID)
	if err != nil || len(items) != 1 || items[0].TextPosterURL != "text.jpg" {
		t.Fatalf("text poster lost after reload: %+v, %v", items, err)
	}
}

func TestMergeListItemsPreservesTextPoster(t *testing.T) {
	base := models.WatchlistItem{ID: "tmdb:movie:12092", MediaType: "movie", PosterURL: "plain.jpg"}
	incoming := base
	incoming.TextPosterURL = "text.jpg"
	merged := mergeListItems(base, incoming)
	if merged.TextPosterURL != incoming.TextPosterURL {
		t.Fatalf("text poster lost when merging aliases: %+v", merged)
	}
	incoming.TextPosterURL = "another.jpg"
	if got := mergeListItems(merged, incoming).TextPosterURL; got != "text.jpg" {
		t.Fatalf("existing text poster replaced: %q", got)
	}
}
