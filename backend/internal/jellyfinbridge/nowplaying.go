package jellyfinbridge

import (
	"context"
	"math"
	"strings"
	"time"

	"novastream/internal/jellyfinbridge/dto"
)

// nowPlayingStale drops a play session from GET /Sessions once its client has
// stopped reporting (clients report progress every few seconds).
const nowPlayingStale = 5 * time.Minute

// activePlay returns the caller device's play session with the most recent
// playback report.
func (s *Server) activePlay(p *principal) (*playSession, playState, bool) {
	s.plays.mu.Lock()
	defer s.plays.mu.Unlock()
	var best *playSession
	var bestState playState
	for _, sess := range s.plays.byID {
		if sess.ProfileID != p.Profile.ID || sess.DeviceID != p.Client.DeviceID {
			continue
		}
		sess.mu.Lock()
		st := sess.state
		sess.mu.Unlock()
		if st == nil || time.Since(st.At) > nowPlayingStale {
			continue
		}
		if best == nil || st.At.After(bestState.At) {
			best, bestState = sess, *st
		}
	}
	return best, bestState, best != nil
}

// fillNowPlaying adds what the device is playing, and how the server prepares
// it, to its session entry: jellyfin-web's playback info reads PlayState and
// TranscodingInfo to show "Direct playing", "Remuxing", "Direct streaming" or
// "Transcoding", like on a real Jellyfin server.
func (s *Server) fillNowPlaying(ctx context.Context, p *principal, info *dto.SessionInfoDto) {
	sess, st, ok := s.activePlay(p)
	if !ok {
		return
	}
	sess.mu.Lock()
	hlsID := sess.currentHLS
	live := sess.Live
	sess.mu.Unlock()

	method := st.PlayMethod
	if method == "" {
		method = "DirectPlay"
		if hlsID != "" {
			method = "Transcode"
		}
	}
	info.PlayState = dto.PlayerStateInfo{
		PositionTicks:       st.PositionTicks,
		CanSeek:             st.CanSeek || !live,
		IsPaused:            st.Paused,
		IsMuted:             st.Muted,
		AudioStreamIndex:    st.Audio,
		SubtitleStreamIndex: st.Subtitle,
		MediaSourceID:       firstNonEmpty(st.MediaSourceID, sess.MediaSourceID),
		PlayMethod:          method,
		RepeatMode:          "RepeatNone",
		PlaybackOrder:       "Default",
	}
	if item, ok := s.itemByID(ctx, p, nil, sess.ItemID, false); ok {
		info.NowPlayingItem = &item
	}
	if method != "Transcode" || hlsID == "" || s.deps.StreamPlan == nil {
		return
	}
	if plan, ok := s.deps.StreamPlan(hlsID); ok {
		info.TranscodingInfo = transcodingInfo(plan)
	}
}

// transcodingInfo maps a mediastorm output plan onto Jellyfin's
// TranscodingInfo. Codecs are reported lowercase, as Jellyfin does.
func transcodingInfo(plan StreamPlan) *dto.TranscodingInfo {
	info := &dto.TranscodingInfo{
		VideoCodec:       strings.ToLower(plan.VideoCodec),
		AudioCodec:       strings.ToLower(plan.AudioCodec),
		Container:        firstNonEmpty(plan.Container, "ts"),
		IsVideoDirect:    plan.VideoCopied,
		IsAudioDirect:    plan.AudioCopied,
		TranscodeReasons: transcodeReasons(plan),
	}
	if plan.EncodeFPS > 0 {
		fps := plan.EncodeFPS
		info.Framerate = &fps
	}
	if plan.CompletionPercentage > 0 {
		pct := math.Min(plan.CompletionPercentage, 100)
		info.CompletionPercentage = &pct
	}
	if !plan.AudioCopied && plan.AudioChannels > 0 {
		channels := plan.AudioChannels
		info.AudioChannels = &channels
	}
	if !plan.VideoCopied {
		info.HardwareAccelerationType = "none"
		if plan.HardwareEncode {
			info.HardwareAccelerationType = strings.ToLower(plan.Encoder)
		}
	}
	return info
}

// transcodeReasons explains, in Jellyfin's TranscodeReason names, why the
// stream is not played directly.
func transcodeReasons(plan StreamPlan) []string {
	reasons := []string{}
	if !plan.VideoCopied && !plan.Live {
		codec := strings.ToLower(plan.SourceVideoCodec)
		switch {
		case plan.SourceDV || plan.SourceHDR:
			reasons = append(reasons, "VideoRangeTypeNotSupported")
		case codec != "" && codec != "h264" && codec != "avc":
			reasons = append(reasons, "VideoCodecNotSupported")
		case strings.Contains(plan.SourcePixFmt, "10") || strings.Contains(plan.SourcePixFmt, "12"):
			reasons = append(reasons, "VideoBitDepthNotSupported")
		}
	}
	if !plan.AudioCopied && plan.SourceAudioCodec != "" && !strings.EqualFold(plan.SourceAudioCodec, "aac") {
		reasons = append(reasons, "AudioCodecNotSupported")
	}
	if len(reasons) == 0 {
		// Both streams fit the browser; only the container is changed (or,
		// for live TV, normalized).
		reasons = append(reasons, "ContainerNotSupported")
	}
	return reasons
}
