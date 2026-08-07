package clients

import (
	"testing"

	"novastream/models"
)

func TestRegisterAndSetNickname(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	client, err := svc.Register("client-1", "user-1", "Apple TV", "tvOS", "1.0", "AppleTV14,1", "Living Room")
	if err != nil {
		t.Fatalf("register client: %v", err)
	}
	if client.Nickname != "Living Room" {
		t.Fatalf("nickname = %q, want Living Room", client.Nickname)
	}

	client, err = svc.SetNickname("client-1", "  Bedroom TV  ")
	if err != nil {
		t.Fatalf("set nickname: %v", err)
	}
	if client.Nickname != "Bedroom TV" {
		t.Fatalf("nickname = %q, want Bedroom TV", client.Nickname)
	}
}

func TestPruneInvalidClientsLockedRemovesOrphans(t *testing.T) {
	svc := &Service{
		clients: map[string]models.Client{
			"valid-client": {
				ID:     "valid-client",
				UserID: "user-1",
			},
			"orphaned-client": {
				ID:     "orphaned-client",
				UserID: "missing-user",
			},
		},
		profiles: map[string]map[string]models.ClientProfileAssociation{
			"valid-client": {
				"user-1": {ClientID: "valid-client", UserID: "user-1"},
			},
			"orphaned-client": {
				"missing-user": {ClientID: "orphaned-client", UserID: "missing-user"},
			},
		},
	}

	validUserIDs := map[string]struct{}{
		"user-1": {},
	}

	removed := svc.pruneInvalidClientsLocked(validUserIDs)

	if len(removed) != 1 {
		t.Fatalf("expected 1 removed client, got %d", len(removed))
	}
	if removed[0].ID != "orphaned-client" {
		t.Fatalf("expected orphaned-client to be removed, got %q", removed[0].ID)
	}
	if _, ok := svc.clients["orphaned-client"]; ok {
		t.Fatal("expected orphaned client to be pruned from service state")
	}
	if _, ok := svc.clients["valid-client"]; !ok {
		t.Fatal("expected valid client to remain in service state")
	}
}

func TestRegisterKeepsMultipleProfileAssociations(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := svc.Register("tv-1", "person-a", "Apple TV", "tvOS", "1.0", "ATV", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register("tv-1", "person-b", "Apple TV", "tvOS", "1.1", "ATV", ""); err != nil {
		t.Fatal(err)
	}
	if !svc.HasProfile("tv-1", "person-a") || !svc.HasProfile("tv-1", "person-b") {
		t.Fatal("expected associations for both people")
	}
	listA := svc.ListByUser("person-a")
	listB := svc.ListByUser("person-b")
	if len(listA) != 1 || len(listB) != 1 {
		t.Fatalf("listA=%d listB=%d, want 1 each", len(listA), len(listB))
	}
	// Expanded list returns two instances of the same device id
	all := svc.List()
	count := 0
	for _, c := range all {
		if c.ID == "tv-1" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("List() instances for tv-1 = %d, want 2", count)
	}
}

func TestUnassignProfileRemovesOnlyOnePerson(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register("tv-1", "person-a", "Apple TV", "tvOS", "1.0", "ATV", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register("tv-1", "person-b", "Apple TV", "tvOS", "1.0", "ATV", ""); err != nil {
		t.Fatal(err)
	}
	deleted, err := svc.UnassignProfile("tv-1", "person-a")
	if err != nil {
		t.Fatal(err)
	}
	if deleted {
		t.Fatal("device should remain while person-b is linked")
	}
	if svc.HasProfile("tv-1", "person-a") {
		t.Fatal("person-a association should be gone")
	}
	if !svc.HasProfile("tv-1", "person-b") {
		t.Fatal("person-b association should remain")
	}
}
