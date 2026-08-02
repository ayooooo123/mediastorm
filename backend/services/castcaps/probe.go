package castcaps

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// Variant is one container/codec combination worth knowing about before a cast
// starts. Keep this list short: every entry costs probe time on a real device.
type Variant string

const (
	// VariantTSAACStereo is the universal baseline. If this fails, the receiver
	// is unreachable or busy rather than picky.
	VariantTSAACStereo Variant = "ts-aac-stereo"
	// VariantFMP4 decides whether the fMP4 container is accepted at all. The
	// asset is H.264, so it proves the container and nothing about codecs.
	VariantFMP4 Variant = "fmp4-aac-stereo"
	// VariantHEVCFMP4 decides whether HEVC video can be copied to the receiver
	// instead of re-encoded to H.264. Container support does not imply this:
	// a second-generation Chromecast accepts fMP4 and cannot decode HEVC.
	VariantHEVCFMP4 Variant = "fmp4-hevc"
	// VariantTSAC3 decides whether Dolby audio can be passed through untouched.
	VariantTSAC3 Variant = "ts-ac3"
	// VariantTSAACMultichannel decides whether re-encoded audio can stay 5.1.
	VariantTSAACMultichannel Variant = "ts-aac51"
)

// AllVariants is the probe order: baseline first so an unreachable or busy
// receiver is detected before spending time on the rest.
var AllVariants = []Variant{VariantTSAACStereo, VariantFMP4, VariantHEVCFMP4, VariantTSAC3, VariantTSAACMultichannel}

// Verdict is what a probe learned about one variant.
type Verdict string

const (
	VerdictSupported Verdict = "supported"
	VerdictRejected  Verdict = "rejected"
	VerdictUnknown   Verdict = "unknown"
)

// Capabilities is the cached answer for one receiver.
type Capabilities struct {
	ReceiverID   string              `json:"receiverId"`
	Host         string              `json:"host"`
	Name         string              `json:"name"`
	Model        string              `json:"model"`
	BuildVersion string              `json:"buildVersion"`
	Variants     map[Variant]Verdict `json:"variants"`
	ProbedAt     time.Time           `json:"probedAt"`
	Partial      bool                `json:"partial,omitempty"`
	Error        string              `json:"error,omitempty"`
}

// Supports answers a single capability question, defaulting to false when the
// receiver has not been probed: the safe Cast envelope (MPEG-TS, stereo AAC-LC)
// is what every receiver tested so far accepts.
func (c *Capabilities) Supports(variant Variant) bool {
	if c == nil {
		return false
	}
	return c.Variants[variant] == VerdictSupported
}

// playbackProofSeconds is how far the playhead must advance before a variant
// counts as supported. A receiver that cannot decode a stream still reports
// BUFFERING, and often a flash of PLAYING at position 0, before erroring.
const playbackProofSeconds = 0.4

// probeVerdictTimeout bounds a single variant. Rejections land in about a
// second; a healthy stream proves itself in three to five.
const probeVerdictTimeout = 12 * time.Second

// ProbeVariant loads one probe stream on a receiver and watches what happens.
func ProbeVariant(ctx context.Context, host, url string) (Verdict, error) {
	c, err := dial(ctx, host)
	if err != nil {
		return VerdictUnknown, err
	}
	defer c.close()

	transportID, err := c.launchMediaReceiver(ctx)
	if err != nil {
		return VerdictUnknown, err
	}
	if err := c.connectTo(transportID); err != nil {
		return VerdictUnknown, fmt.Errorf("connect to media app: %w", err)
	}

	load := map[string]any{
		"type":      "LOAD",
		"requestId": c.nextRequestID(),
		"autoplay":  true,
		"media": map[string]any{
			"contentId":   url,
			"contentType": "application/vnd.apple.mpegurl",
			"streamType":  "BUFFERED",
		},
	}
	payload, err := json.Marshal(load)
	if err != nil {
		return VerdictUnknown, err
	}
	if err := c.send(castMessage{
		SourceID:      c.sourceID,
		DestinationID: transportID,
		Namespace:     namespaceMedia,
		Payload:       string(payload),
	}); err != nil {
		return VerdictUnknown, fmt.Errorf("load: %w", err)
	}

	verdict, err := c.awaitVerdict(ctx, url)
	// Leave the receiver as we found it rather than parked on a test pattern.
	_ = c.send(castMessage{
		SourceID:      c.sourceID,
		DestinationID: transportID,
		Namespace:     namespaceMedia,
		Payload:       fmt.Sprintf(`{"type":"STOP","requestId":%d}`, c.nextRequestID()),
	})
	return verdict, err
}

func (c *conn) awaitVerdict(ctx context.Context, url string) (Verdict, error) {
	deadline := time.After(probeVerdictTimeout)
	for {
		select {
		case <-ctx.Done():
			return VerdictUnknown, ctx.Err()
		case <-deadline:
			// Neither playing nor erroring: treat as unsupported-but-unproven so
			// a flaky probe never promotes a receiver beyond the safe envelope.
			return VerdictUnknown, nil
		case msg, ok := <-c.incoming:
			if !ok {
				return VerdictUnknown, fmt.Errorf("connection closed: %v", c.readErr)
			}
			if msg.Namespace != namespaceMedia {
				continue
			}
			var envelope mediaStatusEnvelope
			if json.Unmarshal([]byte(msg.Payload), &envelope) != nil {
				continue
			}
			if envelope.Type == "LOAD_FAILED" || envelope.Type == "LOAD_CANCELLED" {
				return VerdictRejected, nil
			}
			for _, status := range envelope.Status {
				// Status for a previous item keeps arriving briefly after a new
				// LOAD; only this probe's content can decide the verdict.
				if status.Media.ContentID != "" && status.Media.ContentID != url {
					continue
				}
				if status.PlayerState == "IDLE" && (status.IdleReason == "ERROR" || status.IdleReason == "CANCELLED") {
					return VerdictRejected, nil
				}
				if status.PlayerState == "PLAYING" && status.CurrentTime > playbackProofSeconds {
					return VerdictSupported, nil
				}
			}
		}
	}
}

// ProbeReceiver walks the variant matrix. The baseline variant gates the rest:
// if a receiver cannot play the universally supported stream it is busy, off,
// or unreachable, and the remaining probes would record false rejections.
func ProbeReceiver(ctx context.Context, device Device, urlForVariant func(Variant) string) Capabilities {
	caps := Capabilities{
		ReceiverID:   device.ID(),
		Host:         device.Host,
		Name:         device.Name,
		Model:        device.Model,
		BuildVersion: device.BuildVersion,
		Variants:     map[Variant]Verdict{},
		ProbedAt:     time.Now(),
	}

	for _, variant := range AllVariants {
		verdict, err := ProbeVariant(ctx, device.Host, urlForVariant(variant))
		if err != nil {
			caps.Variants[variant] = VerdictUnknown
			caps.Partial = true
			caps.Error = err.Error()
			log.Printf("[castcaps] receiver %s (%s): variant %s probe failed: %v", device.Name, device.Host, variant, err)
			return caps
		}
		caps.Variants[variant] = verdict
		log.Printf("[castcaps] receiver %s (%s): %s -> %s", device.Name, device.Host, variant, verdict)

		if variant == VariantTSAACStereo && verdict != VerdictSupported {
			// Nothing below the baseline is trustworthy.
			caps.Partial = true
			caps.Error = "baseline variant did not play; receiver busy or unreachable"
			return caps
		}
		// Give the receiver a moment to settle between loads.
		select {
		case <-ctx.Done():
			caps.Partial = true
			return caps
		case <-time.After(time.Second):
		}
	}
	return caps
}
