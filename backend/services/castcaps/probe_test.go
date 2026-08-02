package castcaps

import (
	"context"
	"testing"
	"time"
)

func TestCapabilities_Supports(t *testing.T) {
	var caps *Capabilities
	if caps.Supports(VariantFMP4) {
		t.Error("nil capabilities should not support anything")
	}

	caps = &Capabilities{
		Variants: map[Variant]Verdict{
			VariantFMP4:              VerdictSupported,
			VariantTSAC3:             VerdictRejected,
			VariantTSAACMultichannel: VerdictUnknown,
		},
	}

	if !caps.Supports(VariantFMP4) {
		t.Error("expected true for supported variant")
	}
	if caps.Supports(VariantTSAC3) {
		t.Error("expected false for rejected variant")
	}
	if caps.Supports(VariantTSAACMultichannel) {
		t.Error("expected false for unknown variant")
	}
	if caps.Supports(VariantTSAACStereo) {
		t.Error("expected false for missing variant")
	}
}

func TestAllVariants_Order(t *testing.T) {
	if len(AllVariants) == 0 || AllVariants[0] != VariantTSAACStereo {
		t.Errorf("baseline variant must be first, got %v", AllVariants)
	}
}

func TestAwaitVerdict(t *testing.T) {
	runVerdictTest := func(t *testing.T, url string, msgs []castMessage, want Verdict) {
		t.Helper()
		c := &conn{
			incoming: make(chan castMessage, len(msgs)+1),
			readErr:  nil,
		}
		for _, msg := range msgs {
			c.incoming <- msg
		}
		// In case the test exhausts messages without deciding, close the channel to trigger the read error branch
		// (though in a real scenario we'd wait for a timeout). We use a short timeout instead.
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		verdict, err := c.awaitVerdict(ctx, url)
		// We ignore ctx errors if the expected verdict is Unknown, since timeout implies Unknown.
		if verdict != want {
			t.Errorf("got verdict %v, want %v (err=%v)", verdict, want, err)
		}
	}

	const testURL = "http://test/stream.m3u8"

	t.Run("playing with zero position is unknown", func(t *testing.T) {
		runVerdictTest(t, testURL, []castMessage{
			{Namespace: namespaceMedia, Payload: `{"type":"MEDIA_STATUS","status":[{"playerState":"PLAYING","currentTime":0.0,"media":{"contentId":"http://test/stream.m3u8"}}]}`},
		}, VerdictUnknown)
	})

	t.Run("playing past proof threshold is supported", func(t *testing.T) {
		runVerdictTest(t, testURL, []castMessage{
			{Namespace: namespaceMedia, Payload: `{"type":"MEDIA_STATUS","status":[{"playerState":"PLAYING","currentTime":0.5,"media":{"contentId":"http://test/stream.m3u8"}}]}`},
		}, VerdictSupported)
	})

	t.Run("idle with error is rejected", func(t *testing.T) {
		runVerdictTest(t, testURL, []castMessage{
			{Namespace: namespaceMedia, Payload: `{"type":"MEDIA_STATUS","status":[{"playerState":"IDLE","idleReason":"ERROR","media":{"contentId":"http://test/stream.m3u8"}}]}`},
		}, VerdictRejected)
	})

	t.Run("load failed is rejected", func(t *testing.T) {
		runVerdictTest(t, testURL, []castMessage{
			{Namespace: namespaceMedia, Payload: `{"type":"LOAD_FAILED"}`},
		}, VerdictRejected)
	})

	t.Run("ignores status for different content", func(t *testing.T) {
		runVerdictTest(t, testURL, []castMessage{
			{Namespace: namespaceMedia, Payload: `{"type":"MEDIA_STATUS","status":[{"playerState":"IDLE","idleReason":"ERROR","media":{"contentId":"http://other/stream.m3u8"}}]}`},
		}, VerdictUnknown)
	})
}
