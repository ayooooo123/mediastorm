package handlers

import "novastream/models"

// PlaybackActivityObserver receives player updates only after they have been
// matched to a currently active stream.
type PlaybackActivityObserver interface {
	HandlePlaybackUpdate(userID string, update models.PlaybackProgressUpdate, percentWatched float64)
}

// playbackObserverFanout delivers one matched playback update to every
// registered consumer.
//
// Live playback has more than one consumer: watch notifications, and the p2p
// integration that seeds what is being watched into the swarm. Neither may
// displace or delay the other, so each consumer is dispatched on its own
// goroutine — the notification service performs its webhook round trips inline,
// and a slow webhook must not hold up a seed (or the reverse).
type playbackObserverFanout []PlaybackActivityObserver

func (f playbackObserverFanout) HandlePlaybackUpdate(userID string, update models.PlaybackProgressUpdate, percentWatched float64) {
	for _, observer := range f {
		go observer.HandlePlaybackUpdate(userID, update, percentWatched)
	}
}

// addPlaybackObserver registers one more consumer alongside whatever is already
// registered, so registration order does not matter. That is what lets main wire
// the p2p integration onto playback activity even though it is built after the
// video handler that owns the HLS manager.
//
// The append copies rather than extends in place: a fanout that is already
// registered may be mid-dispatch on another goroutine.
func addPlaybackObserver(registered, observer PlaybackActivityObserver) PlaybackActivityObserver {
	if observer == nil {
		return registered
	}
	switch current := registered.(type) {
	case nil:
		return observer
	case playbackObserverFanout:
		return append(current[:len(current):len(current)], observer)
	default:
		return playbackObserverFanout{current, observer}
	}
}
