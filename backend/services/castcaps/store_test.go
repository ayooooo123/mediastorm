package castcaps

import (
	"context"
	"testing"
	"time"
)

func TestStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	store1 := NewStore(dir)

	caps := Capabilities{
		ReceiverID:   "uuid-1234",
		Host:         "10.0.0.5",
		BuildVersion: "1.0",
		Variants: map[Variant]Verdict{
			VariantFMP4: VerdictSupported,
		},
		ProbedAt: time.Now().Truncate(time.Second), // truncate to match JSON precision
	}
	store1.put(caps)

	store2 := NewStore(dir)
	cached := store2.Lookup("10.0.0.5")
	if cached == nil {
		t.Fatal("expected to find record by host")
	}
	if cached.ReceiverID != caps.ReceiverID || cached.Variants[VariantFMP4] != VerdictSupported || !cached.ProbedAt.Equal(caps.ProbedAt) {
		t.Errorf("round trip mismatch: got %+v, want %+v", cached, caps)
	}

	all := store2.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 record, got %d", len(all))
	}
}

func TestStore_NeedsProbe(t *testing.T) {
	store := NewStore(t.TempDir())

	dev := Device{UUID: "uuid-1", Host: "10.0.0.1", BuildVersion: "v1"}

	if !store.NeedsProbe(dev) {
		t.Error("expected true when no record exists")
	}

	store.put(Capabilities{
		ReceiverID:   "uuid-1",
		Host:         "10.0.0.1",
		BuildVersion: "v1",
		Partial:      true,
		ProbedAt:     time.Now(),
	})
	if !store.NeedsProbe(dev) {
		t.Error("expected true when record is partial")
	}

	store.put(Capabilities{
		ReceiverID:   "uuid-1",
		Host:         "10.0.0.1",
		BuildVersion: "v1",
		ProbedAt:     time.Now().Add(-recheckAfter - time.Hour),
	})
	if !store.NeedsProbe(dev) {
		t.Error("expected true when record is old")
	}

	store.put(Capabilities{
		ReceiverID:   "uuid-1",
		Host:         "10.0.0.1",
		BuildVersion: "v0.9", // different build
		ProbedAt:     time.Now(),
	})
	if !store.NeedsProbe(dev) {
		t.Error("expected true when build version differs")
	}

	store.put(Capabilities{
		ReceiverID:   "uuid-1",
		Host:         "10.0.0.1",
		BuildVersion: "v1",
		ProbedAt:     time.Now(),
	})
	if store.NeedsProbe(dev) {
		t.Error("expected false for fresh complete record with matching build")
	}
}

func TestStore_ProbeRequiresURLBuilder(t *testing.T) {
	store := NewStore(t.TempDir())
	_, err := store.Probe(context.Background(), Device{Host: "10.0.0.1"})
	if err == nil || err.Error() != "probe URL builder is not configured" {
		t.Errorf("expected URL builder error, got %v", err)
	}
}

func TestDevice_ID(t *testing.T) {
	d1 := Device{UUID: "uuid-1", Host: "10.0.0.1"}
	if d1.ID() != "uuid-1" {
		t.Errorf("expected UUID, got %s", d1.ID())
	}

	d2 := Device{Host: "10.0.0.2"}
	if d2.ID() != "10.0.0.2" {
		t.Errorf("expected host fallback, got %s", d2.ID())
	}
}
