package users_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"novastream/models"
	"novastream/services/users"
)

func TestServiceInitialisesDefaultUser(t *testing.T) {
	svc, err := users.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	list := svc.List()
	if len(list) != 1 {
		t.Fatalf("expected exactly one user, got %d", len(list))
	}

	if list[0].ID == "" {
		t.Fatal("expected default user to have an ID")
	}
	if list[0].Name != models.DefaultUserName {
		t.Fatalf("expected default user name %q, got %q", models.DefaultUserName, list[0].Name)
	}
}

func TestServiceCreateRenameAndDelete(t *testing.T) {
	svc, err := users.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	created, err := svc.Create("Evening Watcher")
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}

	if created.ID == "" {
		t.Fatalf("expected created user to have id")
	}

	renamed, err := svc.Rename(created.ID, "Night Owl")
	if err != nil {
		t.Fatalf("rename returned error: %v", err)
	}

	if renamed.Name != "Night Owl" {
		t.Fatalf("expected renamed user to have updated name, got %q", renamed.Name)
	}

	if err := svc.Delete(created.ID); err != nil {
		t.Fatalf("delete returned error: %v", err)
	}

	if svc.Exists(created.ID) {
		t.Fatalf("expected user to be deleted")
	}
}

func TestSetAllowShareLinks(t *testing.T) {
	svc, err := users.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	created, err := svc.Create("Sharer")
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}
	if created.AllowShareLinks {
		t.Fatalf("expected new profile to default to allowShareLinks=false")
	}

	updated, err := svc.SetAllowShareLinks(created.ID, true)
	if err != nil {
		t.Fatalf("SetAllowShareLinks returned error: %v", err)
	}
	if !updated.AllowShareLinks {
		t.Fatalf("expected allowShareLinks=true after grant")
	}

	got, ok := svc.Get(created.ID)
	if !ok || !got.AllowShareLinks {
		t.Fatalf("expected persisted allowShareLinks=true, got ok=%v value=%v", ok, got.AllowShareLinks)
	}

	if _, err := svc.SetAllowShareLinks("missing-id", true); err == nil {
		t.Fatalf("expected error for unknown profile id")
	}
}

func TestActivityPrivacyDefaultsPrivateAndPersistsOptIn(t *testing.T) {
	storageDir := t.TempDir()
	svc, err := users.NewService(storageDir)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	created, err := svc.Create("Watcher")
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}
	if created.ActivityPrivacy != models.ActivityPrivacyNotShared {
		t.Fatalf("activity privacy = %q, want private default", created.ActivityPrivacy)
	}

	updated, err := svc.SetActivityPrivacy(created.ID, models.ActivityPrivacySharedAnonymous)
	if err != nil {
		t.Fatalf("SetActivityPrivacy returned error: %v", err)
	}
	if updated.ActivityPrivacy != models.ActivityPrivacySharedAnonymous {
		t.Fatalf("activity privacy = %q, want anonymous sharing", updated.ActivityPrivacy)
	}
	if _, err := svc.SetActivityPrivacy(created.ID, "public"); err == nil {
		t.Fatal("expected invalid activity privacy to fail")
	}

	reloaded, err := users.NewService(storageDir)
	if err != nil {
		t.Fatalf("reload service: %v", err)
	}
	got, ok := reloaded.Get(created.ID)
	if !ok || got.ActivityPrivacy != models.ActivityPrivacySharedAnonymous {
		t.Fatalf("persisted privacy = %q, ok=%v", got.ActivityPrivacy, ok)
	}
}

func TestLegacyActivityPrivacyMigratesToPrivate(t *testing.T) {
	storageDir := t.TempDir()
	raw := `[{"id":"legacy","accountId":"default","name":"Legacy","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}]`
	if err := os.WriteFile(filepath.Join(storageDir, "users.json"), []byte(raw), 0o600); err != nil {
		t.Fatalf("write legacy users: %v", err)
	}

	svc, err := users.NewService(storageDir)
	if err != nil {
		t.Fatalf("load legacy users: %v", err)
	}
	got, ok := svc.Get("legacy")
	if !ok || got.ActivityPrivacy != models.ActivityPrivacyNotShared {
		t.Fatalf("legacy privacy = %q, ok=%v; want private", got.ActivityPrivacy, ok)
	}
}

func TestSetPinStoresAndClearsPinLength(t *testing.T) {
	storageDir := t.TempDir()
	svc, err := users.NewService(storageDir)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	userID := svc.List()[0].ID
	updated, err := svc.SetPin(userID, "12345")
	if err != nil {
		t.Fatalf("SetPin returned error: %v", err)
	}
	if updated.PinLength != 5 {
		t.Fatalf("expected pin length 5, got %d", updated.PinLength)
	}

	got, ok := svc.Get(userID)
	if !ok {
		t.Fatalf("expected user to exist")
	}
	if got.PinLength != 5 {
		t.Fatalf("expected stored pin length 5, got %d", got.PinLength)
	}

	usersPath := filepath.Join(storageDir, "users.json")
	rawUsers, err := os.ReadFile(usersPath)
	if err != nil {
		t.Fatalf("read users file: %v", err)
	}
	var storedUsers []map[string]any
	if err := json.Unmarshal(rawUsers, &storedUsers); err != nil {
		t.Fatalf("decode users file: %v", err)
	}
	for i := range storedUsers {
		if storedUsers[i]["id"] == userID {
			storedUsers[i]["pinLength"] = 0
		}
	}
	rawUsers, err = json.Marshal(storedUsers)
	if err != nil {
		t.Fatalf("encode users file: %v", err)
	}
	if err := os.WriteFile(usersPath, rawUsers, 0o644); err != nil {
		t.Fatalf("write users file: %v", err)
	}

	reloaded, err := users.NewService(storageDir)
	if err != nil {
		t.Fatalf("failed to reload service: %v", err)
	}
	if err := reloaded.VerifyPin(userID, "12345"); err != nil {
		t.Fatalf("VerifyPin returned error: %v", err)
	}
	repaired, ok := reloaded.Get(userID)
	if !ok {
		t.Fatalf("expected reloaded user to exist")
	}
	if repaired.PinLength != 5 {
		t.Fatalf("expected verify to repair pin length to 5, got %d", repaired.PinLength)
	}

	cleared, err := svc.ClearPin(userID)
	if err != nil {
		t.Fatalf("ClearPin returned error: %v", err)
	}
	if cleared.PinLength != 0 {
		t.Fatalf("expected cleared pin length 0, got %d", cleared.PinLength)
	}
}

func TestDeleteLastUserFails(t *testing.T) {
	svc, err := users.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	list := svc.List()
	if len(list) != 1 {
		t.Fatalf("expected exactly one user, got %d", len(list))
	}

	if err := svc.Delete(list[0].ID); err == nil {
		t.Fatal("expected delete to fail for last remaining user")
	}
}

func TestSetIconURLSendsUserAgent(t *testing.T) {
	var receivedUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "image/png")
		// Return a minimal valid PNG (1x1 transparent pixel)
		png := []byte{
			0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
			0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
			0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
			0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
			0x89, 0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41, // IDAT chunk
			0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
			0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
			0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, // IEND chunk
			0x42, 0x60, 0x82,
		}
		w.Write(png)
	}))
	defer server.Close()

	svc, err := users.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	list := svc.List()
	userID := list[0].ID

	_, err = svc.SetIconURL(userID, server.URL+"/test.png")
	if err != nil {
		t.Fatalf("SetIconURL failed: %v", err)
	}

	if receivedUserAgent == "" {
		t.Fatal("expected User-Agent header to be set, got empty string")
	}
	if receivedUserAgent != "mediastorm/1.0" {
		t.Fatalf("expected User-Agent 'mediastorm/1.0', got %q", receivedUserAgent)
	}
}

func TestSetIconURLInvalidURL(t *testing.T) {
	svc, err := users.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	list := svc.List()
	userID := list[0].ID

	_, err = svc.SetIconURL(userID, "not-a-url")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}

	_, err = svc.SetIconURL(userID, "")
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

func TestSetIconURLUserNotFound(t *testing.T) {
	svc, err := users.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	_, err = svc.SetIconURL("nonexistent-user", "https://example.com/image.png")
	if err == nil {
		t.Fatal("expected error for nonexistent user")
	}
}

func TestSetIconURLServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	svc, err := users.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	list := svc.List()
	userID := list[0].ID

	_, err = svc.SetIconURL(userID, server.URL+"/test.png")
	if err == nil {
		t.Fatal("expected error for server 403 response")
	}
}
