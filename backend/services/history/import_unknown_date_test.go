package history

import (
	"testing"
	"time"

	"novastream/models"
)

// An import without a play date (e.g. Jellyfin items missing DatePlayed) must
// not re-watch a locally unwatched item, but still adds items missing locally.
func TestImportWatchHistory_UnknownWatchedAtKeepsLocalState(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	userID := "user-1"
	watched := true
	unwatched := false
	movie := func(itemID, tmdb string, state *bool) models.WatchHistoryUpdate {
		return models.WatchHistoryUpdate{
			MediaType:   "movie",
			ItemID:      itemID,
			Name:        itemID,
			Watched:     state,
			ExternalIDs: map[string]string{"tmdb": tmdb},
		}
	}

	seed := movie("tmdb:movie:438631", "438631", &watched)
	seed.WatchedAt = time.Now().UTC().Add(-48 * time.Hour)
	if _, err := svc.UpdateWatchHistory(userID, seed); err != nil {
		t.Fatalf("seed watched: %v", err)
	}
	if _, err := svc.UpdateWatchHistory(userID, movie("tmdb:movie:438631", "438631", &unwatched)); err != nil {
		t.Fatalf("seed unwatch: %v", err)
	}

	unwatchedImport := movie("tmdb:movie:438631", "438631", &watched)
	unwatchedImport.WatchedAtUnknown = true
	newImport := movie("tmdb:movie:329865", "329865", &watched)
	newImport.WatchedAtUnknown = true

	imported, err := svc.ImportWatchHistory(userID, []models.WatchHistoryUpdate{unwatchedImport, newImport})
	if err != nil {
		t.Fatalf("ImportWatchHistory() error = %v", err)
	}
	if imported != 1 {
		t.Fatalf("imported = %d, want only the new item", imported)
	}

	item, err := svc.GetWatchHistoryItem(userID, "movie", "tmdb:movie:438631")
	if err != nil || item == nil {
		t.Fatalf("GetWatchHistoryItem() = %v, %v", item, err)
	}
	if item.Watched {
		t.Fatal("local unwatch was overridden by an import without a play date")
	}
	added, err := svc.GetWatchHistoryItem(userID, "movie", "tmdb:movie:329865")
	if err != nil || added == nil || !added.Watched {
		t.Fatalf("new item = %#v, %v; want imported as watched", added, err)
	}
}
