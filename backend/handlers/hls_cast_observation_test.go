package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"novastream/services/castcaps"
)

// The legacy dongle measured today. Its address only has to be stable across
// the fake requests; nothing in these tests contacts it.
const observedReceiverHost = "192.168.8.108"

func newObservationManager(t *testing.T) (*HLSManager, *castcaps.Store) {
	t.Helper()
	store := castcaps.NewStore(t.TempDir())
	manager := &HLSManager{}
	manager.SetCastCapabilities(store)
	return manager, store
}

func newObservedCastSession(host string, fingerprint castVariantFingerprint) *HLSSession {
	return &HLSSession{
		ID:               "cast-observation",
		CastMode:         true,
		CastReceiverHost: host,
		castVariants:     fingerprint,
	}
}

// receiverFetch is a segment request as the receiver itself makes it: from the
// receiver's own address, which is how the server tells it apart from the
// sender app warming the backlog.
func receiverFetch(host, segmentName string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/video/hls/cast-observation/"+segmentName, nil)
	r.RemoteAddr = host + ":41234"
	return r
}

func verdictFor(t *testing.T, store *castcaps.Store, host string, variant castcaps.Variant) castcaps.Verdict {
	t.Helper()
	caps := store.Lookup(host)
	if caps == nil {
		return castcaps.VerdictUnknown
	}
	return caps.Variants[variant]
}

// rewindFirstFetch moves the start of the fetch timeline into the past so a
// sustained-playback window can be exercised without a sleeping test.
func rewindFirstFetch(session *HLSSession, by time.Duration) {
	session.mu.Lock()
	session.castFirstFetch = session.castFirstFetch.Add(-by)
	session.mu.Unlock()
}

// rewindLastFetch ages the most recent fetch so the receiver looks silent.
func rewindLastFetch(session *HLSSession, by time.Duration) {
	session.mu.Lock()
	session.castLastFetch = session.castLastFetch.Add(-by)
	session.mu.Unlock()
}

func TestCastObservationRecordsSupportedOnSustainedPlayback(t *testing.T) {
	manager, store := newObservationManager(t)
	session := newObservedCastSession(observedReceiverHost, castVariantFingerprint{
		Primary: castcaps.VariantTSAACMultichannel,
		Implied: []castcaps.Variant{castcaps.VariantTSAACStereo},
	})

	manager.noteCastReceiverPlaylist(session, receiverFetch(observedReceiverHost, "stream.m3u8"))
	for i := range 10 {
		manager.noteCastReceiverFetch(session, receiverFetch(observedReceiverHost, fmt.Sprintf("segment%d.ts", i)), fmt.Sprintf("segment%d.ts", i))
	}

	// Twenty seconds of media in an instant is a warm-up burst, which is also
	// exactly what a receiver about to stall does. Nothing may be decided yet.
	if got := verdictFor(t, store, observedReceiverHost, castcaps.VariantTSAACMultichannel); got != castcaps.VerdictUnknown {
		t.Fatalf("burst of 10 segments already recorded %q; want no verdict", got)
	}

	// The eleventh fetch arrives sixteen seconds after the first: the receiver
	// is consuming the stream in real time, not buffering and giving up.
	rewindFirstFetch(session, 16*time.Second)
	manager.noteCastReceiverFetch(session, receiverFetch(observedReceiverHost, "segment10.ts"), "segment10.ts")

	if got := verdictFor(t, store, observedReceiverHost, castcaps.VariantTSAACMultichannel); got != castcaps.VerdictSupported {
		t.Fatalf("primary variant verdict = %q, want %q", got, castcaps.VerdictSupported)
	}
	if got := verdictFor(t, store, observedReceiverHost, castcaps.VariantTSAACStereo); got != castcaps.VerdictSupported {
		t.Fatalf("implied variant verdict = %q, want %q", got, castcaps.VerdictSupported)
	}
}

func TestCastObservationRecordsRejectedOnAcceptThenStall(t *testing.T) {
	manager, store := newObservationManager(t)
	// The measured 4K HEVC direct-copy signature: the load is accepted, a few
	// segments are pulled, and playback never starts.
	session := newObservedCastSession(observedReceiverHost, castVariantFingerprint{
		Primary: castcaps.VariantHEVCFMP4,
		Implied: []castcaps.Variant{castcaps.VariantFMP4},
	})

	manager.noteCastReceiverPlaylist(session, receiverFetch(observedReceiverHost, "stream.m3u8"))
	manager.noteCastReceiverFetch(session, receiverFetch(observedReceiverHost, "init.mp4"), "init.mp4")
	for i := range 3 {
		manager.noteCastReceiverFetch(session, receiverFetch(observedReceiverHost, fmt.Sprintf("segment%d.m4s", i)), fmt.Sprintf("segment%d.m4s", i))
	}

	// Ten seconds of keepalives with no fetch is not yet a stall: a receiver
	// buffering a slow source looks identical.
	rewindLastFetch(session, 10*time.Second)
	manager.noteCastKeepalive(session, time.Now())
	if got := verdictFor(t, store, observedReceiverHost, castcaps.VariantHEVCFMP4); got != castcaps.VerdictUnknown {
		t.Fatalf("10s of silence already recorded %q; want no verdict", got)
	}

	// Twenty-one seconds of silence while the sender keeps the session alive is
	// the accept-then-stall signature.
	rewindLastFetch(session, 11*time.Second)
	manager.noteCastKeepalive(session, time.Now())

	if got := verdictFor(t, store, observedReceiverHost, castcaps.VariantHEVCFMP4); got != castcaps.VerdictRejected {
		t.Fatalf("primary variant verdict = %q, want %q", got, castcaps.VerdictRejected)
	}
	// A stall is blamed on the primary variant only. Blaming the container for
	// an HEVC decode failure would bar H.264 fMP4 on a receiver that plays it.
	if got := verdictFor(t, store, observedReceiverHost, castcaps.VariantFMP4); got != castcaps.VerdictUnknown {
		t.Fatalf("implied variant was blamed for the stall: %q", got)
	}
}

func TestCastObservationRecordsNothingForAmbiguousSession(t *testing.T) {
	manager, store := newObservationManager(t)
	session := newObservedCastSession(observedReceiverHost, castVariantFingerprint{
		Primary: castcaps.VariantTSAACStereo,
	})

	manager.noteCastReceiverPlaylist(session, receiverFetch(observedReceiverHost, "stream.m3u8"))
	for i := range 4 {
		manager.noteCastReceiverFetch(session, receiverFetch(observedReceiverHost, fmt.Sprintf("segment%d.ts", i)), fmt.Sprintf("segment%d.ts", i))
	}
	manager.noteCastKeepalive(session, time.Now())

	if records := store.All(); len(records) != 0 {
		t.Fatalf("short session wrote %d capability record(s): %+v", len(records), records)
	}
}

func TestCastObservationRecordsNothingWithoutReceiverHost(t *testing.T) {
	manager, store := newObservationManager(t)
	session := newObservedCastSession("", castVariantFingerprint{Primary: castcaps.VariantTSAACStereo})

	// A cast session with no receiver address cannot attribute anything, so
	// even fetches that look like the receiver's are ignored.
	manager.noteCastReceiverPlaylist(session, receiverFetch(observedReceiverHost, "stream.m3u8"))
	for i := range 12 {
		manager.noteCastReceiverFetch(session, receiverFetch(observedReceiverHost, fmt.Sprintf("segment%d.ts", i)), fmt.Sprintf("segment%d.ts", i))
	}

	// Even a timeline that would otherwise be decisive stays unrecorded.
	now := time.Now()
	session.mu.Lock()
	session.castPlaylistFetched = true
	session.castFetchedSegments = map[int]struct{}{}
	for i := range 12 {
		session.castFetchedSegments[i] = struct{}{}
	}
	session.castFirstFetch = now.Add(-30 * time.Second)
	session.castLastFetch = now
	session.mu.Unlock()
	manager.gradeCastSession(session, now)

	if records := store.All(); len(records) != 0 {
		t.Fatalf("hostless session wrote %d capability record(s): %+v", len(records), records)
	}
}

func TestGradeCastPlayback(t *testing.T) {
	now := time.Date(2026, 8, 2, 20, 0, 0, 0, time.UTC)
	sustained := castPlaybackObservation{
		Now:       now,
		Alive:     true,
		Keepalive: now.Add(-2 * time.Second),
		Playlist:  true,
		First:     now.Add(-18 * time.Second),
		Last:      now,
		Segments:  10,
	}
	stalled := castPlaybackObservation{
		Now:       now,
		Alive:     true,
		Keepalive: now.Add(-2 * time.Second),
		Playlist:  true,
		First:     now.Add(-25 * time.Second),
		Last:      now.Add(-21 * time.Second),
		Segments:  3,
	}

	mutate := func(base castPlaybackObservation, f func(*castPlaybackObservation)) castPlaybackObservation {
		f(&base)
		return base
	}

	for _, tc := range []struct {
		name string
		obs  castPlaybackObservation
		want castcaps.Verdict
	}{
		{"sustained playback is proof", sustained, castcaps.VerdictSupported},
		{"20s of media in a burst proves nothing", mutate(sustained, func(o *castPlaybackObservation) {
			o.First = o.Last.Add(-3 * time.Second)
		}), castcaps.VerdictUnknown},
		{"a long thin trickle is not 20s of media", mutate(sustained, func(o *castPlaybackObservation) {
			o.Segments = 9
		}), castcaps.VerdictUnknown},
		{"accept then stall is a rejection", stalled, castcaps.VerdictRejected},
		{"19s of silence is still buffering", mutate(stalled, func(o *castPlaybackObservation) {
			o.Last = o.Now.Add(-19 * time.Second)
		}), castcaps.VerdictUnknown},
		{"six segments is past the stall envelope", mutate(stalled, func(o *castPlaybackObservation) {
			o.Segments = 6
		}), castcaps.VerdictUnknown},
		{"a dead session says nothing", mutate(stalled, func(o *castPlaybackObservation) {
			o.Alive = false
		}), castcaps.VerdictUnknown},
		{"a gone sender says nothing", mutate(stalled, func(o *castPlaybackObservation) {
			o.Keepalive = o.Now.Add(-90 * time.Second)
		}), castcaps.VerdictUnknown},
		{"no keepalive at all says nothing", mutate(stalled, func(o *castPlaybackObservation) {
			o.Keepalive = time.Time{}
		}), castcaps.VerdictUnknown},
		{"a receiver that never read the playlist says nothing", mutate(stalled, func(o *castPlaybackObservation) {
			o.Playlist = false
		}), castcaps.VerdictUnknown},
		{"no fetches at all says nothing", mutate(stalled, func(o *castPlaybackObservation) {
			o.Segments = 0
			o.First = time.Time{}
			o.Last = time.Time{}
		}), castcaps.VerdictUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, evidence := gradeCastPlayback(tc.obs)
			if got != tc.want {
				t.Fatalf("gradeCastPlayback = %q (%s), want %q", got, evidence, tc.want)
			}
			if got != castcaps.VerdictUnknown && evidence == "" {
				t.Fatal("a recorded verdict must carry the evidence that decided it")
			}
		})
	}
}

func TestCastVariantsForPlan(t *testing.T) {
	for _, tc := range []struct {
		name        string
		plan        castVariantPlan
		wantPrimary castcaps.Variant
		wantImplied []castcaps.Variant
	}{
		{
			name:        "TS with stereo AAC is the baseline variant",
			plan:        castVariantPlan{VideoCodec: "h264", AudioCodec: "aac", AudioChannels: 2},
			wantPrimary: castcaps.VariantTSAACStereo,
		},
		{
			name:        "TS with 5.1 AAC also proves stereo",
			plan:        castVariantPlan{VideoCodec: "h264", AudioCodec: "aac", AudioChannels: 6},
			wantPrimary: castcaps.VariantTSAACMultichannel,
			wantImplied: []castcaps.Variant{castcaps.VariantTSAACStereo},
		},
		{
			name:        "copied Dolby in TS is the AC-3 variant",
			plan:        castVariantPlan{VideoCodec: "h264", AudioCodec: "eac3"},
			wantPrimary: castcaps.VariantTSAC3,
		},
		{
			name: "copied AAC of unknown channel count grades nothing",
			plan: castVariantPlan{VideoCodec: "h264", AudioCodec: "aac"},
		},
		{
			name:        "H.264 in fMP4 is the container variant",
			plan:        castVariantPlan{Fmp4: true, VideoCodec: "h264", AudioCodec: "aac", AudioChannels: 2},
			wantPrimary: castcaps.VariantFMP4,
		},
		{
			name:        "HEVC in fMP4 also proves the container",
			plan:        castVariantPlan{Fmp4: true, VideoCodec: "hevc", AudioCodec: "aac", AudioChannels: 2},
			wantPrimary: castcaps.VariantHEVCFMP4,
			wantImplied: []castcaps.Variant{castcaps.VariantFMP4},
		},
		{
			name: "an unrecognized codec grades nothing",
			plan: castVariantPlan{Fmp4: true, VideoCodec: "mpeg4", AudioCodec: "aac", AudioChannels: 2},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := castVariantsForPlan(tc.plan)
			if got.Primary != tc.wantPrimary {
				t.Fatalf("primary = %q, want %q", got.Primary, tc.wantPrimary)
			}
			if len(got.Implied) != len(tc.wantImplied) {
				t.Fatalf("implied = %v, want %v", got.Implied, tc.wantImplied)
			}
			for i, want := range tc.wantImplied {
				if got.Implied[i] != want {
					t.Fatalf("implied[%d] = %q, want %q", i, got.Implied[i], want)
				}
			}
			if got.empty() != (tc.wantPrimary == "") {
				t.Fatalf("empty() = %v for primary %q", got.empty(), got.Primary)
			}
		})
	}
}
