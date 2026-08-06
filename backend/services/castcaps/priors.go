package castcaps

import (
	"strconv"
	"strings"
)

// modernCastMajor/modernCastMinor separates the two Cast stacks we have
// measured: 192.168.8.108 reports 1.56.291998, the webOS panel 1.68.cast_...
// 1.60 is the round number between them. It distinguishes "known legacy, refuse
// everything" from "unknown, attempt nothing beyond the baseline" — NOT
// "modern, assume more". Both measured devices rejected fMP4 and AC-3, the
// 1.68 panel included, so a newer stack buys no assumption.
const (
	modernCastMajor = 1
	modernCastMinor = 60
)

// legacyModelNames are receivers known to be the gen1/gen2 Cast stack. Google
// shipped every early dongle - and the TV-integrated Cast built from the same
// firmware - as "Eureka Dongle".
var legacyModelNames = []string{"eureka dongle"}

// PriorFor maps a receiver identity onto assumed verdicts. Pure function, no I/O.
//
// Every verdict it returns is a guess from published identity, never a
// measurement, so callers must treat VerdictAssumed as "worth attempting" and
// not as "safe to rely on". Only three envelopes are worth stating, because only
// three are defensible from measured hardware:
//
//   - Legacy Cast stack: MPEG-TS with stereo AAC-LC and nothing else. Measured on
//     a 1.56 dongle, which stalls silently on fMP4 and refuses AC-3 in TS.
//   - Modern receiver: TS baseline, fMP4, HEVC in fMP4 and multichannel AAC.
//     Measured on a webOS panel at 1.68.
//   - Anything unidentifiable: the baseline only.
//
// VariantTSAC3 is left unknown everywhere. AC-3 in MPEG-TS was rejected by both
// measured devices, including the panel with a Dolby decoder, so Dolby
// passthrough is never assumed for anyone.
func PriorFor(id Identity) map[Variant]Verdict {
	if isLegacyCast(id) {
		return map[Variant]Verdict{
			VariantTSAACStereo:       VerdictAssumed,
			VariantFMP4:              VerdictRejected,
			VariantHEVCFMP4:          VerdictRejected,
			VariantTSAC3:             VerdictRejected,
			VariantTSAACMultichannel: VerdictRejected,
		}
	}
	// Everything else, modern stacks included: the baseline only. Every other
	// variant stays VerdictUnknown, which reads as "do not attempt". The one
	// modern receiver we have measured rejected fMP4 with an explicit ERROR in
	// 1.0s, so "the firmware is new" is not evidence that anything beyond the
	// baseline works. An upgrade needs an observed VerdictSupported, which the
	// playback grader can only record for a variant we actually sent — so the
	// upgrade path opens when real evidence arrives, not on a guess whose
	// failure mode is a receiver that stalls with no error.
	return map[Variant]Verdict{VariantTSAACStereo: VerdictAssumed}
}

// isLegacyCast reports the gen1/gen2/TV-integrated-old-Cast envelope, either by
// model name or by a build revision older than the modern cutoff.
func isLegacyCast(id Identity) bool {
	model := strings.ToLower(strings.TrimSpace(id.ModelName))
	for _, legacy := range legacyModelNames {
		if model == legacy {
			return true
		}
	}
	if major, minor, ok := parseBuildRevision(id.BuildRevision); ok {
		return !atLeast(major, minor, modernCastMajor, modernCastMinor)
	}
	return false
}

// buildRevisionAtLeast reports whether a receiver's cast_build_revision is at
// least major.minor. An unparseable or missing revision reports false: absence
// of evidence is not evidence of a modern receiver.
func buildRevisionAtLeast(revision string, major, minor int) bool {
	gotMajor, gotMinor, ok := parseBuildRevision(revision)
	if !ok {
		return false
	}
	return atLeast(gotMajor, gotMinor, major, minor)
}

func atLeast(gotMajor, gotMinor, wantMajor, wantMinor int) bool {
	if gotMajor != wantMajor {
		return gotMajor > wantMajor
	}
	return gotMinor >= wantMinor
}

// parseBuildRevision reads the major and minor of a cast_build_revision. Real
// devices report two very different shapes - "1.56.291998" on a dongle and
// "1.68.cast_20250829_0200_RC13.800768591" on a webOS panel - so everything past
// the minor is ignored rather than parsed.
func parseBuildRevision(revision string) (major, minor int, ok bool) {
	fields := strings.Split(strings.TrimSpace(revision), ".")
	if len(fields) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(strings.TrimSpace(fields[0]))
	if err != nil {
		return 0, 0, false
	}
	minor, err = strconv.Atoi(strings.TrimSpace(fields[1]))
	if err != nil {
		return 0, 0, false
	}
	if major < 0 || minor < 0 {
		return 0, 0, false
	}
	return major, minor, true
}
