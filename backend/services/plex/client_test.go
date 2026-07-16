package plex

import (
	"encoding/json"
	"testing"
)

func TestPlexLibraryItemAcceptsLegacyAndProviderGUIDs(t *testing.T) {
	var item PlexLibraryItem
	err := json.Unmarshal([]byte(`{
		"ratingKey":"10",
		"guid":"plex://movie/abc",
		"Guid":[{"id":"imdb://tt1234567"},{"id":"tmdb://42"}]
	}`), &item)
	if err != nil {
		t.Fatalf("unmarshal Plex item: %v", err)
	}
	if item.GUID != "plex://movie/abc" {
		t.Fatalf("GUID = %q", item.GUID)
	}
	if len(item.Guid) != 2 || item.Guid[0].ID != "imdb://tt1234567" || item.Guid[1].ID != "tmdb://42" {
		t.Fatalf("provider GUIDs = %#v", item.Guid)
	}
}
