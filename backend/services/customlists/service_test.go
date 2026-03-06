package customlists

import (
	"testing"

	"novastream/models"
)

func TestCustomListsCRUD(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	userID := "user-1"
	list, err := svc.CreateList(userID, "Weekend")
	if err != nil {
		t.Fatalf("create list: %v", err)
	}
	if list.Name != "Weekend" {
		t.Fatalf("unexpected list name: %q", list.Name)
	}

	renamed, err := svc.RenameList(userID, list.ID, "Weekend Picks")
	if err != nil {
		t.Fatalf("rename list: %v", err)
	}
	if renamed.Name != "Weekend Picks" {
		t.Fatalf("unexpected renamed list name: %q", renamed.Name)
	}

	_, err = svc.AddItem(userID, list.ID, models.WatchlistUpsert{
		ID:        "movie-1",
		MediaType: "movie",
		Name:      "Interstellar",
		Year:      2014,
	})
	if err != nil {
		t.Fatalf("add item: %v", err)
	}

	items, err := svc.ListItems(userID, list.ID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Name != "Interstellar" {
		t.Fatalf("unexpected item name: %q", items[0].Name)
	}

	lists, err := svc.ListLists(userID)
	if err != nil {
		t.Fatalf("list lists: %v", err)
	}
	if len(lists) != 1 {
		t.Fatalf("expected 1 list, got %d", len(lists))
	}
	if lists[0].ItemCount != 1 {
		t.Fatalf("expected itemCount 1, got %d", lists[0].ItemCount)
	}

	removed, err := svc.RemoveItem(userID, list.ID, "movie", "movie-1")
	if err != nil {
		t.Fatalf("remove item: %v", err)
	}
	if !removed {
		t.Fatalf("expected removed=true")
	}

	deleted, err := svc.DeleteList(userID, list.ID)
	if err != nil {
		t.Fatalf("delete list: %v", err)
	}
	if !deleted {
		t.Fatalf("expected deleted=true")
	}
}
