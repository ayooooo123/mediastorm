package castcaps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const (
	lgHost     = "192.168.8.105"
	dongleHost = "192.168.8.108"
)

var (
	lgIdentity = Identity{
		Host:          lgHost,
		Name:          "[LG] webOS TV OLED65C2PUA",
		BuildRevision: "1.68.cast_20250829_0200_RC13.800768591",
		UDN:           "1b2c3d4e-5f60-4718-8293-a4b5c6d7e8f9",
	}
	dongleIdentity = Identity{
		Host:          dongleHost,
		Name:          "Avery's room TV",
		ModelName:     "Eureka Dongle",
		Manufacturer:  "Google Inc.",
		BuildRevision: "1.56.291998",
		UDN:           "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a",
	}
)

// storeWith returns a store in a temp dir whose identity source is canned, so
// no test ever contacts a receiver.
func storeWith(t *testing.T, identities ...Identity) *Store {
	t.Helper()
	store := NewStore(t.TempDir())
	byHost := map[string]Identity{}
	for _, identity := range identities {
		byHost[identity.Host] = identity
	}
	store.describeFn = func(_ context.Context, host string) (Identity, error) {
		identity, ok := byHost[host]
		if !ok {
			return Identity{}, errors.New("no receiver answered")
		}
		return identity, nil
	}
	return store
}

func TestStore_Record_Precedence(t *testing.T) {
	tests := []struct {
		name  string
		seed  []Verdict // applied in order before the verdict under test
		apply Verdict
		want  Verdict
	}{
		{"observation over nothing", nil, VerdictSupported, VerdictSupported},
		{"prior over nothing", nil, VerdictAssumed, VerdictAssumed},
		{"observation over prior", []Verdict{VerdictAssumed}, VerdictSupported, VerdictSupported},
		{"rejection over prior", []Verdict{VerdictAssumed}, VerdictRejected, VerdictRejected},
		{"prior never overwrites a proven variant", []Verdict{VerdictSupported}, VerdictAssumed, VerdictSupported},
		{"prior never overwrites a rejection", []Verdict{VerdictRejected}, VerdictAssumed, VerdictRejected},
		{"rejection overwrites proven support", []Verdict{VerdictSupported}, VerdictRejected, VerdictRejected},
		{"support does not undo a rejection", []Verdict{VerdictRejected}, VerdictSupported, VerdictRejected},
		{"unknown records nothing", []Verdict{VerdictAssumed}, VerdictUnknown, VerdictAssumed},
		{"unrecognized verdict records nothing", []Verdict{VerdictSupported}, "bogus", VerdictSupported},
		{"repeat observation is a no-op", []Verdict{VerdictSupported}, VerdictSupported, VerdictSupported},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := storeWith(t)
			for _, seed := range tc.seed {
				store.Record(lgHost, VariantFMP4, seed)
			}
			store.Record(lgHost, VariantFMP4, tc.apply)

			if got := store.Lookup(lgHost).Verdict(VariantFMP4); got != tc.want {
				t.Fatalf("verdict = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStore_Record_PersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	store.Record(dongleHost, VariantTSAACStereo, VerdictSupported)
	store.Record(dongleHost, VariantFMP4, VerdictRejected)

	reopened := NewStore(dir)
	caps := reopened.Lookup(dongleHost)
	if caps == nil {
		t.Fatal("record did not survive a reopen")
	}
	if !caps.Supports(VariantTSAACStereo) {
		t.Fatal("supported verdict lost")
	}
	if caps.Verdict(VariantFMP4) != VerdictRejected {
		t.Fatalf("fmp4 = %q, want rejected", caps.Verdict(VariantFMP4))
	}
	if caps.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt not set")
	}
}

func TestStore_Record_IgnoresEmptyInput(t *testing.T) {
	store := storeWith(t)
	store.Record("", VariantFMP4, VerdictSupported)
	store.Record("  ", VariantFMP4, VerdictSupported)
	store.Record(lgHost, "", VerdictSupported)
	if len(store.All()) != 0 {
		t.Fatalf("expected no records, got %v", store.All())
	}
}

func TestStore_Ensure_AppliesPriorsWithoutClobberingObservations(t *testing.T) {
	store := storeWith(t, lgIdentity)
	// A real cast proved fMP4 works and HEVC did not, before we ever described
	// the panel.
	store.Record(lgHost, VariantFMP4, VerdictSupported)
	store.Record(lgHost, VariantHEVCFMP4, VerdictRejected)

	caps, err := store.Ensure(context.Background(), lgHost)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if caps.Identity != lgIdentity {
		t.Fatalf("identity = %#v, want %#v", caps.Identity, lgIdentity)
	}
	if caps.Verdict(VariantFMP4) != VerdictSupported {
		t.Fatalf("prior clobbered an observation: fmp4 = %q", caps.Verdict(VariantFMP4))
	}
	if caps.Verdict(VariantHEVCFMP4) != VerdictRejected {
		t.Fatalf("prior clobbered a rejection: hevc = %q", caps.Verdict(VariantHEVCFMP4))
	}
	// Gaps are filled from the prior, which for any non-legacy receiver is the
	// baseline alone: a modern build earns no assumption, so 5.1 AAC and AC-3
	// both stay unknown until a real session proves them.
	if caps.Verdict(VariantTSAACMultichannel) != VerdictUnknown {
		t.Fatalf("aac51 = %q, want unknown", caps.Verdict(VariantTSAACMultichannel))
	}
	if caps.Verdict(VariantTSAC3) != VerdictUnknown {
		t.Fatalf("ts-ac3 = %q, want unknown", caps.Verdict(VariantTSAC3))
	}
	if !caps.Allows(VariantTSAACStereo) || caps.Supports(VariantTSAACStereo) {
		t.Fatal("an assumed variant must be allowed but not supported")
	}
}

func TestStore_Ensure_LegacyDongleEnvelope(t *testing.T) {
	store := storeWith(t, dongleIdentity)
	caps, err := store.Ensure(context.Background(), dongleHost)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !caps.Allows(VariantTSAACStereo) || caps.Supports(VariantTSAACStereo) {
		t.Fatal("baseline should be assumed, not proven")
	}
	for _, variant := range []Variant{VariantFMP4, VariantHEVCFMP4, VariantTSAC3, VariantTSAACMultichannel} {
		if caps.Allows(variant) {
			t.Fatalf("legacy dongle must not allow %s", variant)
		}
	}
}

func TestStore_Ensure_FirmwareChangeDiscardsObservations(t *testing.T) {
	store := storeWith(t, dongleIdentity)
	if _, err := store.Ensure(context.Background(), dongleHost); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	store.Record(dongleHost, VariantFMP4, VerdictRejected)

	// Same box, new firmware. The observed rejection is discarded because the
	// firmware that earned it is gone, but a newer build is still not evidence:
	// the variant returns to unknown, not to assumed.
	upgraded := dongleIdentity
	upgraded.ModelName = "Chromecast HD"
	upgraded.BuildRevision = "1.68.cast_20250829_0200_RC13.800768591"
	store.describeFn = func(context.Context, string) (Identity, error) { return upgraded, nil }

	caps, err := store.Ensure(context.Background(), dongleHost)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if caps.Verdict(VariantFMP4) != VerdictUnknown {
		t.Fatalf("fmp4 = %q, want unknown after the firmware that rejected it was replaced", caps.Verdict(VariantFMP4))
	}
	if caps.Allows(VariantFMP4) {
		t.Fatal("a discarded rejection must not become permission")
	}
	if caps.BuildRevision != upgraded.BuildRevision {
		t.Fatalf("build revision = %q, want %q", caps.BuildRevision, upgraded.BuildRevision)
	}
}

func TestStore_Ensure_UnreachableReceiver(t *testing.T) {
	store := storeWith(t)
	if _, err := store.Ensure(context.Background(), "10.0.0.99"); err == nil {
		t.Fatal("expected an error when nothing answers and nothing is cached")
	}
	if store.Lookup("10.0.0.99") != nil {
		t.Fatal("a receiver that never answered must not be cached")
	}

	// With something on file, a silent receiver falls back to what we know.
	store.Record("10.0.0.99", VariantTSAACStereo, VerdictSupported)
	caps, err := store.Ensure(context.Background(), "10.0.0.99")
	if err != nil {
		t.Fatalf("Ensure with cache: %v", err)
	}
	if !caps.Supports(VariantTSAACStereo) {
		t.Fatal("cached observation lost")
	}
}

func TestStore_Ensure_EmptyHost(t *testing.T) {
	if _, err := storeWith(t).Ensure(context.Background(), "   "); err == nil {
		t.Fatal("expected an error for an empty host")
	}
}

func TestStore_LookupReturnsACopy(t *testing.T) {
	store := storeWith(t)
	store.Record(lgHost, VariantFMP4, VerdictSupported)

	caps := store.Lookup(lgHost)
	caps.Variants[VariantFMP4] = VerdictRejected

	if !store.Lookup(lgHost).Supports(VariantFMP4) {
		t.Fatal("mutating a lookup result must not corrupt the cache")
	}
	if store.Lookup("10.0.0.1") != nil {
		t.Fatal("unknown host must look up nil")
	}
}

func TestStore_NilReceiverIsSafe(t *testing.T) {
	var store *Store
	if store.Lookup(lgHost) != nil {
		t.Fatal("nil store must look up nil")
	}
	if store.All() != nil {
		t.Fatal("nil store must have no records")
	}
	store.Record(lgHost, VariantFMP4, VerdictSupported)
	store.RefreshInBackground(context.Background(), "192.168.8.0/24")
	if _, err := store.Ensure(context.Background(), lgHost); err == nil {
		t.Fatal("nil store must fail Ensure")
	}
}

func TestStore_LoadsLegacyCacheFile(t *testing.T) {
	dir := t.TempDir()
	// Exactly the shape the probing version of this package wrote, including a
	// verdict string that no longer exists.
	legacy := `[
	  {
	    "receiverId": "uuid-1",
	    "host": "192.168.8.108",
	    "name": "Avery's room TV",
	    "model": "Eureka Dongle",
	    "buildVersion": "1.56.291998",
	    "variants": {
	      "ts-aac-stereo": "supported",
	      "fmp4-aac-stereo": "rejected",
	      "fmp4-hevc": "unknown"
	    },
	    "probedAt": "2026-08-01T22:14:05Z",
	    "partial": true,
	    "error": "baseline variant did not play"
	  }
	]`
	if err := os.WriteFile(filepath.Join(dir, legacyCacheFile), []byte(legacy), 0o600); err != nil {
		t.Fatalf("seed legacy cache: %v", err)
	}

	caps := NewStore(dir).Lookup(dongleHost)
	if caps == nil {
		t.Fatal("legacy cache entry was dropped")
	}
	if !caps.Supports(VariantTSAACStereo) || caps.Verdict(VariantFMP4) != VerdictRejected {
		t.Fatalf("legacy verdicts lost: %v", caps.Variants)
	}
	if caps.Verdict(VariantHEVCFMP4) != VerdictUnknown {
		t.Fatalf(`legacy "unknown" must normalize to the empty verdict, got %q`, caps.Verdict(VariantHEVCFMP4))
	}
	if caps.ModelName != "Eureka Dongle" || caps.BuildRevision != "1.56.291998" {
		t.Fatalf("legacy identity lost: %#v", caps.Identity)
	}
	if caps.UpdatedAt.IsZero() {
		t.Fatal("legacy probedAt should carry over as UpdatedAt")
	}
}

func TestStore_ToleratesUnreadableCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, recordFileName(lgHost)), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, legacyCacheFile), []byte("nope"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	store := NewStore(dir)
	if len(store.All()) != 0 {
		t.Fatalf("expected the corrupt entries to be ignored, got %v", store.All())
	}
	// A missing directory is equally fine: nothing has been cached yet.
	fresh := NewStore(filepath.Join(dir, "does-not-exist"))
	if len(fresh.All()) != 0 {
		t.Fatal("expected an empty store")
	}
	fresh.Record(lgHost, VariantTSAACStereo, VerdictSupported)
	if !fresh.Lookup(lgHost).Supports(VariantTSAACStereo) {
		t.Fatal("store should create its directory on first write")
	}
}

func TestRecordFileName(t *testing.T) {
	tests := map[string]string{
		"192.168.8.105":     "receiver-192.168.8.105.json",
		"fe80::1%25en0":     "receiver-fe80__1_25en0.json",
		"living-room.local": "receiver-living-room.local.json",
		"../escape":         "receiver-.._escape.json",
	}
	for host, want := range tests {
		if got := recordFileName(host); got != want {
			t.Fatalf("recordFileName(%q) = %q, want %q", host, got, want)
		}
	}
}
