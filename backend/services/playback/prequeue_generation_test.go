package playback

import (
	"testing"
	"time"
)

// Clearing every entry marks a change to how releases rank; callers that
// keep resolved streams of their own compare the generation.
func TestPrequeueGenerationCountsClears(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	before := store.Generation()
	store.DeleteAll()
	if store.Generation() != before+1 {
		t.Fatalf("generation = %d after a clear, want %d", store.Generation(), before+1)
	}
}

// A profile's ranking or filtering change clears only its entries, and
// moves the generation too.
func TestPrequeueGenerationCountsUserClears(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	before := store.Generation()
	store.DeleteByUser("user1")
	if store.Generation() != before+1 {
		t.Fatalf("generation = %d after a user clear, want %d", store.Generation(), before+1)
	}
}
