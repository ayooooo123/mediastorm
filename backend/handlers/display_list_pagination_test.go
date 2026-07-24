package handlers

import (
	"net/url"
	"testing"

	"novastream/models"
)

func TestCappedDisplayListQueryLimitsDiscoveryResults(t *testing.T) {
	query := url.Values{"limit": {"5000"}, "offset": {"450"}}
	capped := cappedDisplayListQuery(query)

	if got := capped.Get("limit"); got != "50" {
		t.Fatalf("limit = %q, want 50", got)
	}
	if got := query.Get("limit"); got != "5000" {
		t.Fatalf("input query was mutated: limit = %q", got)
	}
}

func TestCapDisplayListPayloadCapsItemsAndReportedTotals(t *testing.T) {
	items := make([]interface{}, 100)
	payload := map[string]interface{}{
		"items":           items,
		"total":           float64(12_000),
		"sourceTotal":     float64(12_000),
		"unfilteredTotal": float64(15_000),
	}

	capDisplayListPayload(payload, url.Values{"offset": {"450"}})

	if got := len(payload["items"].([]interface{})); got != 50 {
		t.Fatalf("items length = %d, want 50", got)
	}
	for _, key := range []string{"total", "sourceTotal", "unfilteredTotal"} {
		if got := payload[key]; got != float64(maxDiscoveryListItems) {
			t.Fatalf("%s = %v, want %d", key, got, maxDiscoveryListItems)
		}
	}
}

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
