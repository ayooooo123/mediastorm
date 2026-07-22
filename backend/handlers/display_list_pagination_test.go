package handlers

import (
	"testing"

	"novastream/models"
)

type paginationHiddenItemsService struct{}

func (paginationHiddenItemsService) List(string) ([]models.HiddenItem, error) { return nil, nil }
func (paginationHiddenItemsService) Hide(string, models.HiddenItemUpsert) (models.HiddenItem, error) {
	return models.HiddenItem{}, nil
}
func (paginationHiddenItemsService) Unhide(string, string, string) (bool, error) { return false, nil }
func (paginationHiddenItemsService) IsHidden(string, string, string, map[string]string) bool {
	return false
}
func (paginationHiddenItemsService) FilterHiddenWatchlistItems(
	_ string,
	items []models.WatchlistItem,
) []models.WatchlistItem {
	return items
}
func (paginationHiddenItemsService) ShouldHideTitleMap(_ string, item map[string]interface{}) bool {
	return item["id"] == "hidden"
}

func TestFilterHiddenPayloadPreservesSourceTotalForPagination(t *testing.T) {
	h := &DisplayListHandler{HiddenItemsService: paginationHiddenItemsService{}}
	payload := map[string]interface{}{
		"total": float64(482),
		"items": []interface{}{
			map[string]interface{}{"title": map[string]interface{}{"id": "visible"}},
			map[string]interface{}{"title": map[string]interface{}{"id": "hidden"}},
		},
	}

	filtered := h.filterHiddenPayload("user-1", payload)
	if got := filtered["sourceTotal"]; got != float64(482) {
		t.Fatalf("sourceTotal = %v, want 482", got)
	}
	if got := filtered["total"]; got != 1 {
		t.Fatalf("visible total = %v, want 1", got)
	}
	items, ok := filtered["items"].([]interface{})
	if !ok || len(items) != 1 {
		t.Fatalf("filtered items = %#v, want one visible item", filtered["items"])
	}
}
