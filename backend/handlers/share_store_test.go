package handlers

import (
	"context"
	"testing"
	"time"
)

func TestShareStoreSingleUseCap(t *testing.T) {
	store := NewShareStore(nil)
	ctx := context.Background()
	rec, err := store.Create(ctx, "acct1", false, map[string]string{"sourcePath": "/movies/a.mkv"}, time.Hour, 1, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.Token == "" {
		t.Fatal("expected a token")
	}

	got, ok := store.Consume(ctx, rec.Token)
	if !ok {
		t.Fatal("first Consume should succeed")
	}
	if got.Params["sourcePath"] != "/movies/a.mkv" {
		t.Fatalf("params not preserved: %v", got.Params)
	}

	if _, ok := store.Consume(ctx, rec.Token); ok {
		t.Fatal("second Consume should fail (max uses = 1)")
	}
}

func TestShareStoreMultiUseUntilCap(t *testing.T) {
	store := NewShareStore(nil)
	ctx := context.Background()
	rec, _ := store.Create(ctx, "acct1", false, map[string]string{"sourcePath": "/a.mkv"}, time.Hour, 3, "")

	for i := 0; i < 3; i++ {
		if _, ok := store.Consume(ctx, rec.Token); !ok {
			t.Fatalf("use %d should succeed", i+1)
		}
	}
	if _, ok := store.Consume(ctx, rec.Token); ok {
		t.Fatal("4th use should fail once cap reached")
	}
}

func TestShareStoreUsesAreHardCapped(t *testing.T) {
	store := NewShareStore(nil)
	ctx := context.Background()

	// A request for 0 ("unlimited") is clamped to the hard cap, not left open-ended.
	rec, _ := store.Create(ctx, "acct1", false, map[string]string{"sourcePath": "/a.mkv"}, time.Hour, 0, "")
	if rec.MaxUses != MaxShareLinkUses {
		t.Fatalf("maxUses=0 should clamp to %d, got %d", MaxShareLinkUses, rec.MaxUses)
	}

	// A request above the cap is clamped down to it.
	rec2, _ := store.Create(ctx, "acct1", false, map[string]string{"sourcePath": "/b.mkv"}, time.Hour, MaxShareLinkUses+50, "")
	if rec2.MaxUses != MaxShareLinkUses {
		t.Fatalf("oversized maxUses should clamp to %d, got %d", MaxShareLinkUses, rec2.MaxUses)
	}

	// The link is exhausted once the cap is reached.
	for i := 0; i < MaxShareLinkUses; i++ {
		if _, ok := store.Consume(ctx, rec.Token); !ok {
			t.Fatalf("use %d within cap should succeed", i+1)
		}
	}
	if _, ok := store.Consume(ctx, rec.Token); ok {
		t.Fatalf("use beyond cap of %d should fail", MaxShareLinkUses)
	}
}

func TestShareStoreConsumeExpired(t *testing.T) {
	store := NewShareStore(nil)
	ctx := context.Background()
	rec, err := store.Create(ctx, "acct1", false, nil, time.Hour, 0, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	mem := store.repo.(*inMemoryShareRepo)
	mem.mu.Lock()
	mem.links[rec.Token].ExpiresAt = time.Now().Add(-time.Minute)
	mem.mu.Unlock()

	if _, ok := store.Consume(ctx, rec.Token); ok {
		t.Fatal("expired share should not be consumable")
	}
}

func TestShareStoreConsumeUnknown(t *testing.T) {
	store := NewShareStore(nil)
	ctx := context.Background()
	if _, ok := store.Consume(ctx, "does-not-exist"); ok {
		t.Fatal("unknown token should not be consumable")
	}
	if _, ok := store.Consume(ctx, ""); ok {
		t.Fatal("empty token should not be consumable")
	}
}

func TestShareStoreSetActiveBlocksConsume(t *testing.T) {
	store := NewShareStore(nil)
	ctx := context.Background()
	rec, _ := store.Create(ctx, "acct1", false, map[string]string{"sourcePath": "/a.mkv"}, time.Hour, 0, "")

	if ok, err := store.SetActive(ctx, "acct1", false, rec.Token, false); err != nil || !ok {
		t.Fatalf("SetActive failed: ok=%v err=%v", ok, err)
	}
	if _, ok := store.Consume(ctx, rec.Token); ok {
		t.Fatal("deactivated link should not be consumable")
	}
}

func TestShareStoreOwnershipScoping(t *testing.T) {
	store := NewShareStore(nil)
	ctx := context.Background()
	mine, _ := store.Create(ctx, "acct1", false, map[string]string{"sourcePath": "/a.mkv"}, time.Hour, 0, "")
	theirs, _ := store.Create(ctx, "acct2", false, map[string]string{"sourcePath": "/b.mkv"}, time.Hour, 0, "")

	// Non-master cannot delete another account's link.
	if ok, _ := store.Delete(ctx, "acct1", false, theirs.Token); ok {
		t.Fatal("should not delete another account's link")
	}
	// Non-master only sees own links.
	links, _ := store.List(ctx, "acct1", false)
	if len(links) != 1 || links[0].Token != mine.Token {
		t.Fatalf("expected only own link, got %d", len(links))
	}
	// Master sees all.
	all, _ := store.List(ctx, "master", true)
	if len(all) != 2 {
		t.Fatalf("master should see all links, got %d", len(all))
	}
}

func TestShareStorePurgeExpired(t *testing.T) {
	store := NewShareStore(nil)
	ctx := context.Background()
	expired, _ := store.Create(ctx, "acct1", false, nil, time.Hour, 0, "")
	live, _ := store.Create(ctx, "acct1", false, nil, time.Hour, 0, "")

	mem := store.repo.(*inMemoryShareRepo)
	mem.mu.Lock()
	mem.links[expired.Token].ExpiresAt = time.Now().Add(-time.Minute)
	mem.mu.Unlock()

	if err := mem.DeleteExpired(ctx, time.Now()); err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}

	mem.mu.Lock()
	defer mem.mu.Unlock()
	if _, ok := mem.links[expired.Token]; ok {
		t.Fatal("expired share should be purged")
	}
	if _, ok := mem.links[live.Token]; !ok {
		t.Fatal("live share should survive purge")
	}
}
