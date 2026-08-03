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
