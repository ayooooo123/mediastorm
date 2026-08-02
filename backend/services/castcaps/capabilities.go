// Package castcaps records what a Cast receiver can actually play, without ever
// taking the receiver away from its owner to find out.
//
// The Cast protocol has no capability query, and receivers lie by omission:
// they accept a load request for a stream they cannot decode, fetch a few segments and
// then sit in BUFFERING forever, or land in IDLE/ERROR seconds later. The
// tempting answer is to load a test clip and watch the playhead, but that hijacks
// whatever the household is watching, so this package does not do it and must
// never do it.
//
// What is left are two honest, silent sources:
//
//   - Identity the device already publishes over plain HTTP on port 8008
//     (eureka_info, and the DIAL device description when served). Mapped through
//     PriorFor, that yields "assumed" verdicts: plausible, unproven.
//   - Observation of playback the user actually requested, fed back through
//     Store.Record as "supported" or "rejected". Those are facts.
//
// Nothing here connects to the CASTV2 port, launches an app, or loads media.
package castcaps

import "time"

// Variant is one container/codec combination worth knowing about before a cast
// starts.
type Variant string

const (
	// VariantTSAACStereo is the universal baseline: MPEG-TS with stereo AAC-LC.
	// Every receiver measured so far plays it, including first-generation
	// hardware, so it is the only thing ever assumed for an unknown device.
	VariantTSAACStereo Variant = "ts-aac-stereo"
	// VariantFMP4 is the fMP4 container carrying H.264. Container support says
	// nothing about codecs: legacy receivers accept the load request and stall silently.
	VariantFMP4 Variant = "fmp4-aac-stereo"
	// VariantHEVCFMP4 decides whether HEVC video can be copied to the receiver
	// instead of re-encoded. Container support does not imply it: a
	// second-generation Chromecast takes fMP4 and cannot decode HEVC.
	VariantHEVCFMP4 Variant = "fmp4-hevc"
	// VariantTSAC3 decides whether Dolby audio can be passed through untouched.
	// Never assumed: AC-3 in TS was rejected by every device measured, including
	// a modern webOS panel that certainly has a Dolby decoder.
	VariantTSAC3 Variant = "ts-ac3"
	// VariantTSAACMultichannel decides whether re-encoded audio can stay 5.1.
	VariantTSAACMultichannel Variant = "ts-aac51"
)

// AllVariants lists every variant, baseline first.
var AllVariants = []Variant{VariantTSAACStereo, VariantFMP4, VariantHEVCFMP4, VariantTSAC3, VariantTSAACMultichannel}

// Verdict is how much we know about one variant on one receiver.
type Verdict string

const (
	// VerdictUnknown means never observed and no prior worth stating.
	VerdictUnknown Verdict = ""
	// VerdictAssumed comes from the model/firmware prior: plausible, unproven.
	VerdictAssumed Verdict = "assumed"
	// VerdictSupported means a real playback session sustained this variant.
	VerdictSupported Verdict = "supported"
	// VerdictRejected means a real playback session failed on this variant:
	// an error, or the accept-then-stall signature.
	VerdictRejected Verdict = "rejected"
)

// verdictRank orders verdicts so a later report can only ever add certainty:
// unknown < assumed < supported < rejected.
//
// Rejected outranking supported is deliberate. A variant that stalled once will
// stall again, and the failure mode is a silent freeze on the user's TV, so one
// measured failure outweighs an earlier success. Firmware updates are the
// legitimate way out of a rejection, and Store.Ensure handles that by clearing
// observations when the build revision changes.
func verdictRank(v Verdict) int {
	switch v {
	case VerdictAssumed:
		return 1
	case VerdictSupported:
		return 2
	case VerdictRejected:
		return 3
	default:
		return 0
	}
}

// normalizeVerdict maps anything unrecognized - including verdict strings
// written by older builds of this package, such as the literal "unknown" - onto
// the current set.
func normalizeVerdict(v Verdict) Verdict {
	switch v {
	case VerdictAssumed, VerdictSupported, VerdictRejected:
		return v
	default:
		return VerdictUnknown
	}
}

// observed reports whether a verdict came from watching real playback.
func observed(v Verdict) bool {
	return v == VerdictSupported || v == VerdictRejected
}

// Capabilities is everything known about one receiver.
type Capabilities struct {
	Identity
	Variants  map[Variant]Verdict `json:"variants"`
	UpdatedAt time.Time           `json:"updatedAt"`
}

// Supports reports proven support only. Use it for any decision whose failure
// mode is a silent stall on the user's screen: a guess is not good enough there.
func (c *Capabilities) Supports(variant Variant) bool {
	if c == nil {
		return false
	}
	return c.Variants[variant] == VerdictSupported
}

// Allows reports proven or assumed support. Use it to decide whether an upgrade
// is worth attempting at all, so an unproven receiver can still earn a fact.
func (c *Capabilities) Allows(variant Variant) bool {
	if c == nil {
		return false
	}
	switch c.Variants[variant] {
	case VerdictSupported, VerdictAssumed:
		return true
	default:
		return false
	}
}

// Verdict returns what is known about one variant.
func (c *Capabilities) Verdict(variant Variant) Verdict {
	if c == nil {
		return VerdictUnknown
	}
	return normalizeVerdict(c.Variants[variant])
}

// clone deep-copies the variant map so callers cannot mutate cached state.
func (c Capabilities) clone() Capabilities {
	copied := c
	copied.Variants = make(map[Variant]Verdict, len(c.Variants))
	for variant, verdict := range c.Variants {
		copied.Variants[variant] = verdict
	}
	return copied
}
