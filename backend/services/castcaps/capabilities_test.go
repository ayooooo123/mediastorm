package castcaps

import (
	"reflect"
	"testing"
)

func TestCapabilities_SupportsVsAllows(t *testing.T) {
	tests := []struct {
		name         string
		verdict      Verdict
		wantSupports bool
		wantAllows   bool
	}{
		// The whole point of the split: an assumed variant is worth attempting
		// but must never gate something that fails as a silent stall.
		{"assumed", VerdictAssumed, false, true},
		{"supported", VerdictSupported, true, true},
		{"rejected", VerdictRejected, false, false},
		{"unknown", VerdictUnknown, false, false},
		{"absent", "no-such-verdict", false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caps := &Capabilities{Variants: map[Variant]Verdict{VariantFMP4: tc.verdict}}
			if got := caps.Supports(VariantFMP4); got != tc.wantSupports {
				t.Fatalf("Supports(%q) = %v, want %v", tc.verdict, got, tc.wantSupports)
			}
			if got := caps.Allows(VariantFMP4); got != tc.wantAllows {
				t.Fatalf("Allows(%q) = %v, want %v", tc.verdict, got, tc.wantAllows)
			}
			// An untouched variant answers no to both regardless.
			if caps.Supports(VariantHEVCFMP4) || caps.Allows(VariantHEVCFMP4) {
				t.Fatal("an unmentioned variant must answer no to both")
			}
		})
	}
}

func TestCapabilities_NilReceiver(t *testing.T) {
	var caps *Capabilities
	for _, variant := range AllVariants {
		if caps.Supports(variant) {
			t.Fatalf("nil Capabilities.Supports(%s) = true", variant)
		}
		if caps.Allows(variant) {
			t.Fatalf("nil Capabilities.Allows(%s) = true", variant)
		}
		if got := caps.Verdict(variant); got != VerdictUnknown {
			t.Fatalf("nil Capabilities.Verdict(%s) = %q", variant, got)
		}
	}
	// A non-nil record with a nil map must be equally safe.
	empty := &Capabilities{}
	if empty.Supports(VariantTSAACStereo) || empty.Allows(VariantTSAACStereo) {
		t.Fatal("empty Capabilities must answer no")
	}
}

func TestCapabilities_VerdictNormalizesLegacyStrings(t *testing.T) {
	// Older builds wrote the literal "unknown"; it must not read back as a
	// fourth state.
	caps := &Capabilities{Variants: map[Variant]Verdict{VariantFMP4: "unknown"}}
	if got := caps.Verdict(VariantFMP4); got != VerdictUnknown {
		t.Fatalf("Verdict = %q, want %q", got, VerdictUnknown)
	}
}

func TestVerdictRankOrdering(t *testing.T) {
	ordered := []Verdict{VerdictUnknown, VerdictAssumed, VerdictSupported, VerdictRejected}
	for i := 1; i < len(ordered); i++ {
		if verdictRank(ordered[i-1]) >= verdictRank(ordered[i]) {
			t.Fatalf("rank(%q) must be below rank(%q)", ordered[i-1], ordered[i])
		}
	}
	if observed(VerdictAssumed) || observed(VerdictUnknown) {
		t.Fatal("only supported/rejected come from observation")
	}
	if !observed(VerdictSupported) || !observed(VerdictRejected) {
		t.Fatal("supported and rejected are observations")
	}
}

func TestAllVariants_BaselineFirst(t *testing.T) {
	want := []Variant{VariantTSAACStereo, VariantFMP4, VariantHEVCFMP4, VariantTSAC3, VariantTSAACMultichannel}
	if !reflect.DeepEqual(AllVariants, want) {
		t.Fatalf("AllVariants = %v, want %v", AllVariants, want)
	}
}
