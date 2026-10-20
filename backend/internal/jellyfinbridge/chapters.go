package jellyfinbridge

import (
	"context"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/services/playback"
)

// Chapters: Jellyfin clients read an item's chapters when playback starts
// (jellyfin-web loads the played items with Fields=Chapters before
// PlaybackInfo) and draw them on the seek bar and as next/previous chapter
// buttons. They belong to the stream, which the item does not know until it
// is resolved, so the bridge remembers the chapters of the stream last probed
// for each profile and item, and probes the prequeued stream as soon as a
// details page has started it.

const chapterPrewarmWait = time.Minute

type chapterSet struct {
	chapters []dto.ChapterInfo
	at       time.Time
}

func chaptersKey(profileID, itemID string) string { return profileID + "|" + itemID }

// rememberChapters keeps the chapters of a probed stream.
func (s *Server) rememberChapters(profileID, itemID string, probe *mediaProbe) {
	if probe == nil || profileID == "" || itemID == "" || len(probe.Chapters) < 2 {
		return
	}
	out := make([]dto.ChapterInfo, 0, len(probe.Chapters))
	for i, c := range probe.Chapters {
		name := c.Title
		if name == "" {
			name = "Kapitel " + itoa(i+1)
		}
		out = append(out, dto.ChapterInfo{StartPositionTicks: secondsToTicks(c.Start), Name: name, ImageDateModified: "0001-01-01T00:00:00.0000000Z"})
	}
	s.chapters.Store(chaptersKey(profileID, itemID), chapterSet{chapters: out, at: time.Now()})
}

type knownDuration struct {
	seconds float64
	at      time.Time
}

// rememberDuration keeps the length of a played stream.
func (s *Server) rememberDuration(profileID, itemID string, seconds float64) {
	if profileID == "" || itemID == "" || seconds <= 0 {
		return
	}
	s.durations.Store(chaptersKey(profileID, itemID), knownDuration{seconds: seconds, at: time.Now()})
}

// knownDuration returns the length of the stream last played for an item,
// or 0.
func (s *Server) knownDuration(profileID, itemID string) float64 {
	v, ok := s.durations.Load(chaptersKey(profileID, itemID))
	if !ok {
		return 0
	}
	d := v.(knownDuration)
	if time.Since(d.at) > playSessionTTL {
		s.durations.Delete(chaptersKey(profileID, itemID))
		return 0
	}
	return d.seconds
}

// withChapters adds known chapters to movies and episodes.
func (s *Server) withChapters(p *principal, items []dto.BaseItemDto) {
	for i := range items {
		item := &items[i]
		if item.Type != dto.KindMovie && item.Type != dto.KindEpisode {
			continue
		}
		v, ok := s.chapters.Load(chaptersKey(p.Profile.ID, item.ID))
		if !ok {
			continue
		}
		set := v.(chapterSet)
		if time.Since(set.at) > playSessionTTL {
			s.chapters.Delete(chaptersKey(p.Profile.ID, item.ID))
			continue
		}
		list := make([]any, len(set.chapters))
		for j, c := range set.chapters {
			list[j] = c
		}
		item.Chapters = list
	}
}

// probePrequeued waits for a prewarmed prequeue and probes its stream, so
// the chapters are known before the client presses play.
func (s *Server) probePrequeued(p *principal, itemID, prequeueID string) {
	ctx, cancel := context.WithTimeout(context.Background(), chapterPrewarmWait+mediaProbeWait)
	defer cancel()
	scratch := &playSession{ItemID: itemID, ProfileID: p.Profile.ID, prequeueID: prequeueID}
	if status := s.awaitPrequeue(ctx, p, scratch, chapterPrewarmWait); status != nil && status.Status == playback.PrequeueStatusReady {
		s.probeMedia(ctx, p, scratch, status)
	}
}

// chaptersPending marks a stream for the item as being resolved and probed;
// the returned func marks it done.
func (s *Server) chaptersPending(profileID, itemID string) func() {
	key := chaptersKey(profileID, itemID)
	done := make(chan struct{})
	s.chapterWaits.Store(key, done)
	return func() {
		s.chapterWaits.CompareAndDelete(key, done)
		close(done)
	}
}

// awaitChapters waits up to wait for a pending probe of the item, unless its
// chapters are known already.
func (s *Server) awaitChapters(ctx context.Context, profileID, itemID string, wait time.Duration) {
	key := chaptersKey(profileID, itemID)
	if _, known := s.chapters.Load(key); known || wait <= 0 {
		return
	}
	v, pending := s.chapterWaits.Load(key)
	if !pending {
		return
	}
	select {
	case <-v.(chan struct{}):
	case <-time.After(wait):
	case <-ctx.Done():
	}
}
