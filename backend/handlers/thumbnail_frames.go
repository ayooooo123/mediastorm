package handlers

import (
	"path/filepath"
	"sort"
)

// ThumbnailFrame is one generated seek thumbnail (a 640px-wide JPEG).
type ThumbnailFrame struct {
	TimeSec float64
	Path    string
}

// ThumbnailsEnabled reports the playback.thumbnails.enabled setting.
func (h *VideoHandler) ThumbnailsEnabled() bool {
	return h != nil && h.thumbnailManager != nil && h.thumbnailGenerationSettings().Enabled
}

// ThumbnailFrames lists the usable thumbnails generated so far for a stream
// path, in time order, and whether generation has finished. In-process
// callers must have authorized access to the path themselves.
func (h *VideoHandler) ThumbnailFrames(path string) ([]ThumbnailFrame, bool) {
	if h == nil || h.thumbnailManager == nil {
		return nil, false
	}
	key := thumbnailKey(cleanVideoPathParam(path))
	manifest, err := h.thumbnailManager.readManifest(key)
	if err != nil {
		return nil, false
	}
	dir := filepath.Join(h.thumbnailManager.baseDir, key)
	frames := make([]ThumbnailFrame, 0, len(manifest.Thumbnails))
	for _, thumb := range manifest.Thumbnails {
		file := filepath.Join(dir, filepath.Base(thumb.File))
		if thumbnailOutputUsable(file) {
			frames = append(frames, ThumbnailFrame{TimeSec: thumb.TimeSec, Path: file})
		}
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i].TimeSec < frames[j].TimeSec })
	done := manifest.Status == "ready" && !h.thumbnailManager.isInflight(key)
	return frames, done
}
