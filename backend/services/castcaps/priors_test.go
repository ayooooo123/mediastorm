package castcaps

import (
	"reflect"
	"testing"
)

func TestParseBuildRevision(t *testing.T) {
	tests := []struct {
		name     string
		revision string
		major    int
		minor    int
		ok       bool
	}{
		// Both shapes are recorded from real hardware.
		{"webos panel", "1.68.cast_20250829_0200_RC13.800768591", 1, 68, true},
		{"legacy dongle", "1.56.291998", 1, 56, true},
		{"two components only", "1.60", 1, 60, true},
		{"padded", " 2.4.7 ", 2, 4, true},
		{"empty", "", 0, 0, false},
		{"single component", "1", 0, 0, false},
		{"non numeric major", "cast.68.1", 0, 0, false},
		{"non numeric minor", "1.x68.3", 0, 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			major, minor, ok := parseBuildRevision(tc.revision)
			if major != tc.major || minor != tc.minor || ok != tc.ok {
				t.Fatalf("parseBuildRevision(%q) = (%d, %d, %v), want (%d, %d, %v)",
					tc.revision, major, minor, ok, tc.major, tc.minor, tc.ok)
			}
		})
	}
}

func TestBuildRevisionAtLeast(t *testing.T) {
	tests := []struct {
		revision string
		major    int
		minor    int
		want     bool
	}{
		{"1.68.cast_20250829_0200_RC13.800768591", 1, 60, true},
		{"1.56.291998", 1, 60, false},
		{"1.60.1", 1, 60, true},
		{"2.0.0", 1, 60, true},
		{"0.99.0", 1, 60, false},
		{"", 1, 60, false},
		{"garbage", 1, 60, false},
	}

	for _, tc := range tests {
		t.Run(tc.revision, func(t *testing.T) {
			if got := buildRevisionAtLeast(tc.revision, tc.major, tc.minor); got != tc.want {
				t.Fatalf("buildRevisionAtLeast(%q, %d, %d) = %v, want %v",
					tc.revision, tc.major, tc.minor, got, tc.want)
			}
		})
	}
}

func TestPriorFor(t *testing.T) {
	legacyEnvelope := map[Variant]Verdict{
		VariantTSAACStereo:       VerdictAssumed,
		VariantFMP4:              VerdictRejected,
		VariantHEVCFMP4:          VerdictRejected,
		VariantTSAC3:             VerdictRejected,
		VariantTSAACMultichannel: VerdictRejected,
	}
	// There is no "modern envelope". The only modern receiver measured (a 1.68
	// webOS panel) rejected fMP4 with an explicit ERROR, so a newer build earns
	// no assumption beyond the baseline every device has been seen to play.
	baselineOnly := map[Variant]Verdict{VariantTSAACStereo: VerdictAssumed}

	tests := []struct {
		name string
		id   Identity
		want map[Variant]Verdict
	}{
		{
			// 192.168.8.108, hardware-confirmed: TS/AAC plays, fMP4 stalls.
			name: "legacy dongle by model and build",
			id:   Identity{Host: "192.168.8.108", Name: "Avery's room TV", ModelName: "Eureka Dongle", BuildRevision: "1.56.291998"},
			want: legacyEnvelope,
		},
		{
			name: "legacy by model name alone",
			id:   Identity{ModelName: "eureka dongle"},
			want: legacyEnvelope,
		},
		{
			name: "legacy by old build alone",
			id:   Identity{Name: "Living Room", BuildRevision: "1.56.291998"},
			want: legacyEnvelope,
		},
		{
			// 192.168.8.105. Modern build, and it still rejected fMP4 on real
			// hardware. A new firmware is not evidence of anything.
			name: "webos panel gets no upgrade from its build revision",
			id:   Identity{Host: "192.168.8.105", Name: "[LG] webOS TV OLED65C2PUA", BuildRevision: "1.68.cast_20250829_0200_RC13.800768591"},
			want: baselineOnly,
		},
		{
			name: "modern build alone assumes nothing extra",
			id:   Identity{Name: "Bedroom", BuildRevision: "1.68.cast_20250829_0200_RC13.800768591"},
			want: baselineOnly,
		},
		{
			name: "recognizable modern model still assumes nothing extra",
			id:   Identity{ModelName: "Chromecast Ultra"},
			want: baselineOnly,
		},
		{
			name: "unidentifiable receiver assumes the baseline only",
			id:   Identity{Host: "10.0.0.9", Name: "10.0.0.9"},
			want: baselineOnly,
		},
		{
			name: "bare chromecast is not promoted",
			id:   Identity{Name: "Chromecast"},
			want: baselineOnly,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PriorFor(tc.id)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("PriorFor(%#v) = %v, want %v", tc.id, got, tc.want)
			}
			if got[VariantTSAC3] == VerdictAssumed {
				t.Fatal("AC-3 passthrough must never be assumed: both measured devices rejected it")
			}
		})
	}
}

func TestPriorFor_ModernLeavesAC3Unknown(t *testing.T) {
	prior := PriorFor(Identity{Name: "[LG] webOS TV OLED65C2PUA", BuildRevision: "1.68.cast_20250829_0200_RC13.800768591"})
	if verdict, present := prior[VariantTSAC3]; present || verdict != VerdictUnknown {
		t.Fatalf("ts-ac3 prior = %q (present %v), want unknown and absent", verdict, present)
	}
}
