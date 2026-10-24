package jellyfinbridge

import (
	"net/http"
	"sync/atomic"
	"testing"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
)

// After a change to how mediastorm ranks releases (its ranking settings), a
// new playback of the same item resolves again instead of reusing the
// stream picked under the old ranking.
func TestRankingChangeIsNotServedFromCaches(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	installFakePlayback(t, h)
	var gen atomic.Uint64
	h.srv.deps.RankingGeneration = gen.Load
	var prequeues atomic.Int32
	prequeue := h.srv.deps.Handlers.Prequeue
	h.srv.deps.Handlers.Prequeue = func(w http.ResponseWriter, r *http.Request) {
		prequeues.Add(1)
		prequeue(w, r)
	}
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)
	play := func() string {
		return decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{})).PlaySessionID
	}
	first := play()
	if again := play(); again != first {
		t.Fatal("PlaybackInfo again without a change must reuse the session")
	}
	started := prequeues.Load()

	gen.Add(1) // the ranking settings were saved
	if after := play(); after == first {
		t.Fatal("a session resolved under the old ranking was reused")
	}
	if prequeues.Load() == started {
		t.Fatal("no new prequeue after the ranking changed")
	}
}

// A PlaybackInfo during playback (a track switch, a seek) keeps the running
// stream after a ranking change, so the release does not change under the
// player.
func TestRankingChangeKeepsRunningPlayback(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	installFakePlayback(t, h)
	var gen atomic.Uint64
	h.srv.deps.RankingGeneration = gen.Load
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)
	play := func() string {
		return decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{})).PlaySessionID
	}
	first := play()
	h.do(http.MethodPost, "/Sessions/Playing/Progress", tok, map[string]any{"ItemId": item, "PlaySessionId": first, "PositionTicks": int64(600_000_000)})

	gen.Add(1) // the ranking settings were saved
	if again := play(); again != first {
		t.Fatal("a running playback was moved to a new session after a ranking change")
	}
}
