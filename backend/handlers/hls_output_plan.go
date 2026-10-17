package handlers

import (
	"math"
	"time"
)

// HLSOutputPlan records what an HLS session's FFmpeg run hands the player:
// which streams are copied and which are re-encoded. It is written once the
// transcode plan is final, so callers (the Jellyfin bridge's playback info)
// report what really happens instead of reconstructing it from flags.
type HLSOutputPlan struct {
	Live bool

	SourceVideoCodec string
	SourcePixFmt     string
	SourceHDR        bool
	SourceDV         bool
	SourceAudioCodec string

	VideoCodec     string // output codec: the source codec when copied, "h264" when encoded
	VideoCopied    bool
	Encoder        string // "copy", "libx264" or a hardware kind
	HardwareEncode bool
	Tonemapped     bool

	AudioCodec    string
	AudioCopied   bool
	AudioChannels int // only known when the audio is encoded

	Container   string // ts | mp4 (fMP4)
	VideoRange  string // SDR | PQ | HLG: what the client gets
	RangeType   string // Jellyfin VideoRangeType of the source (stable copy)
	DolbyVision string // stable copy Dolby Vision handling: keep | strip

	// CompletionPercentage is how far the output reaches into the title (VOD
	// only); filled in when the plan is read, not recorded.
	CompletionPercentage float64
	// SourceFPS is the source frame rate; EncodeFPS the current run's output
	// frames per second (0 when unknown, finished or not encoding).
	SourceFPS float64
	EncodeFPS float64
}

// recordOutputPlan stores the final plan of a VOD FFmpeg run on the session.
func (m *HLSManager) recordOutputPlan(session *HLSSession, sourceVideoCodec string, videoEncoded, webPlan bool, plan videoEncodePlan,
	audioStreams []audioStreamInfo, audioCodec string, audioChannels int, audioCopied bool) {
	out := HLSOutputPlan{
		SourceVideoCodec: sourceVideoCodec,
		VideoCodec:       sourceVideoCodec,
		VideoCopied:      !videoEncoded,
		Encoder:          "copy",
		AudioCodec:       audioCodec,
		AudioCopied:      audioCopied,
		AudioChannels:    audioChannels,
	}
	if videoEncoded {
		out.VideoCodec = "h264"
		out.Encoder = "libx264"
		if webPlan && plan.HardwareEncode {
			out.Encoder = string(plan.Kind)
			out.HardwareEncode = true
		}
		out.Tonemapped = webPlan && plan.Tonemapped
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if selected, ok := selectedAudioStream(audioStreams, session.AudioTrackIndex); ok {
		out.SourceAudioCodec = selected.Codec
	}
	if session.ProbeData != nil {
		out.SourcePixFmt = session.ProbeData.VideoPixFmt
		if fps, ok := parseVideoFrameRate(session.ProbeData.AvgFrameRate); ok {
			out.SourceFPS = fps
		}
	}
	out.SourceHDR, out.SourceDV = session.HasHDR, session.HasDV
	out.Container = "ts"
	if session.SegmentExt == ".m4s" || session.SegmentExt == ".mp4" {
		out.Container = "mp4"
	}
	out.VideoRange = "SDR"
	if session.usesStableCopyTimeline() {
		out.VideoRange = session.CopyPlan.OutputRange
		out.RangeType = session.CopyPlan.RangeType
		out.DolbyVision = session.CopyPlan.DV
	} else if !videoEncoded && (session.HasHDR || session.HasDV) {
		out.VideoRange = "PQ"
	}
	session.outputPlan = &out
}

// recordLiveOutputPlan stores the plan of a live run: native targets copy
// both streams, every other target gets the H.264/AAC compatibility encode.
func recordLiveOutputPlan(session *HLSSession, transmux bool) {
	out := HLSOutputPlan{Live: true, Encoder: "copy", VideoCopied: true, AudioCopied: true}
	if !transmux {
		out = HLSOutputPlan{Live: true, Encoder: "libx264", VideoCodec: "h264", AudioCodec: "aac"}
	}
	session.outputPlan = &out
}

// SessionOutputPlan returns the recorded output plan of an HLS session. It is
// false until the session's first FFmpeg run has been planned.
func (m *HLSManager) SessionOutputPlan(sessionID string) (HLSOutputPlan, bool) {
	session, ok := m.GetSession(sessionID)
	if !ok {
		return HLSOutputPlan{}, false
	}
	session.mu.RLock()
	if session.outputPlan == nil {
		session.mu.RUnlock()
		return HLSOutputPlan{}, false
	}
	plan := *session.outputPlan
	completed, duration := session.Completed, session.Duration
	runStart, runOffset := session.runStartedAt, session.TranscodingOffset
	session.mu.RUnlock()
	if !plan.Live && duration > 0 {
		plan.CompletionPercentage = 100
		if !completed {
			until := m.encodedUntil(session)
			plan.CompletionPercentage = until / duration * 100
			plan.EncodeFPS = encodeFPS(until-runOffset, time.Since(runStart), plan.SourceFPS)
		}
	}
	return plan, true
}

// encodeFPS converts media seconds produced over wall time into output frames
// per second at the source frame rate.
func encodeFPS(produced float64, elapsed time.Duration, sourceFPS float64) float64 {
	if produced <= 0 || elapsed < time.Second || sourceFPS <= 0 {
		return 0
	}
	return math.Round(produced/elapsed.Seconds()*sourceFPS*10) / 10
}

// encodedUntil is the media time the session's output reaches: the end of
// the highest segment written so far.
func (m *HLSManager) encodedUntil(session *HLSSession) float64 {
	highest := m.findHighestSegmentNumber(session)
	if highest < 0 {
		return 0
	}
	if session.usesStableCopyTimeline() {
		if end, ok := session.stableCopySegmentStart(highest + 1); ok {
			return end
		}
		return session.Duration
	}
	return math.Min(float64(highest+1)*hlsSegmentDuration, session.Duration)
}

// HLSOutputPlan exposes SessionOutputPlan for in-process callers.
func (h *VideoHandler) HLSOutputPlan(sessionID string) (HLSOutputPlan, bool) {
	if h.hlsManager == nil {
		return HLSOutputPlan{}, false
	}
	return h.hlsManager.SessionOutputPlan(sessionID)
}
