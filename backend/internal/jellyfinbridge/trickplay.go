package jellyfinbridge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/services/playback"
)

// Trickplay (scrubbing previews) is built from mediastorm's seek thumbnails:
// single 640px frames every 30-60 s, generated in the background when
// playback.thumbnails is enabled. Jellyfin clients expect tile grids at a
// fixed interval, so the bridge composes 10x10 tiles of 320x180 cells on
// request, filling each slot with the nearest generated frame.
const (
	trickplayWidth      = 320
	trickplayHeight     = 180
	trickplayTileSide   = 10
	trickplayIntervalMS = 30_000 // mediastorm's finest thumbnail spacing
	trickplayCacheBytes = 32 << 20
)

// ThumbnailFrame is one generated seek thumbnail.
type ThumbnailFrame struct {
	TimeSec float64
	Path    string
}

// mediaSourceID is the media source ID of an item for one profile and
// device. Jellyfin clients key trickplay by media source and read it from the
// item before PlaybackInfo, so the ID must be known when the item is listed.
// It is an HMAC with a per-process key: stable for the device, unguessable
// for anyone else (token-less media requests use it as a capability).
func (s *Server) mediaSourceID(profileID, deviceID, itemID string) string {
	mac := hmac.New(sha256.New, s.msKey)
	fmt.Fprintf(mac, "%s|%s|%s", profileID, deviceID, itemID)
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// withTrickplay adds trickplay info to playable VOD items.
func (s *Server) withTrickplay(p *principal, items []dto.BaseItemDto) {
	if s.deps.ThumbnailsEnabled == nil || !s.deps.ThumbnailsEnabled() {
		return
	}
	for i := range items {
		item := &items[i]
		if item.Type != dto.KindMovie && item.Type != dto.KindEpisode || item.RunTimeTicks == nil || *item.RunTimeTicks <= 0 {
			continue
		}
		if ref, ok := s.ids.get(item.ID); !ok || !playableVOD(ref.Kind) {
			continue
		}
		runtimeMS := *item.RunTimeTicks / 10_000
		count := int((runtimeMS + trickplayIntervalMS - 1) / trickplayIntervalMS)
		info := map[string]dto.TrickplayInfo{
			strconv.Itoa(trickplayWidth): {
				Width: trickplayWidth, Height: trickplayHeight,
				TileWidth: trickplayTileSide, TileHeight: trickplayTileSide,
				ThumbnailCount: count, Interval: trickplayIntervalMS,
				Bandwidth: trickplayWidth * trickplayHeight * trickplayTileSide * trickplayTileSide / 10,
			},
		}
		// Every version (release) has its own media source ID.
		// The item ID is the automatic version's ID in version lists and
		// PlaybackInfo answers.
		item.Trickplay = map[string]map[string]dto.TrickplayInfo{s.mediaSourceID(p.Profile.ID, p.Client.DeviceID, item.ID): info, item.ID: info}
		for _, source := range item.MediaSources {
			item.Trickplay[source.ID] = info
		}
	}
}

// playableVOD reports whether a ref is a single video file: streamed movies
// and episodes and local library files.
func playableVOD(kind string) bool {
	return kind == refMovie || kind == refEpisode || kind == refLocal
}

// wantsTrickplay reports whether a list request asked for trickplay data
// (single-item requests always get it, like on Jellyfin).
func wantsTrickplay(r *http.Request) bool {
	return containsFold(queryList(r, "Fields"), "Trickplay")
}

// startThumbnails asks mediastorm to generate seek thumbnails for a resolved
// stream, once per play session.
func (s *Server) startThumbnails(p *principal, sess *playSession, status *playback.PrequeueStatusResponse) {
	if s.deps.Handlers.ThumbnailsStart == nil || s.deps.ThumbnailsEnabled == nil || !s.deps.ThumbnailsEnabled() ||
		status == nil || status.Status != playback.PrequeueStatusReady || status.StreamPath == "" ||
		!playableVOD(sess.Ref.Kind) {
		return
	}
	sess.mu.Lock()
	started := sess.thumbnailsStarted
	sess.thumbnailsStarted = true
	sess.mu.Unlock()
	if started {
		return
	}
	q := url.Values{"path": {status.StreamPath}, "profileId": {p.Profile.ID}}
	if status.Duration > 0 {
		q.Set("duration", strconv.FormatFloat(status.Duration, 'f', 3, 64))
	}
	if status.HasDolbyVision {
		q.Set("dv", "true")
		if status.DolbyVisionProfile != "" {
			q.Set("dvProfile", status.DolbyVisionProfile)
		}
	}
	if status.HasHDR10 {
		q.Set("hdr", "true")
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if code, err := callJSON(s.deps.Handlers.ThumbnailsStart, internalRequest(ctx, p, http.MethodPost, "/api/video/thumbnails/start?"+q.Encode(), nil, nil), nil); err != nil {
			log.Printf("[jellyfin-bridge] thumbnails for %s: %d %v", sess.ItemID, code, err)
		}
	}()
}

// handleTrickplayTile serves /Videos/{id}/Trickplay/{width}/{index}.jpg.
func (s *Server) handleTrickplayTile(w http.ResponseWriter, r *http.Request) {
	itemID := normalizeID(pathVar(r, "id"))
	sess, ok := s.mediaSession(r, itemID, queryValue(r, "MediaSourceId"))
	if !ok || s.deps.ThumbnailFrames == nil {
		writeError(w, http.StatusNotFound, "No trickplay data")
		return
	}
	if _, ok := s.principalForSession(r, sess); !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	index, err := strconv.Atoi(pathVar(r, "index"))
	if err != nil || index < 0 || pathVar(r, "width") != strconv.Itoa(trickplayWidth) {
		writeError(w, http.StatusNotFound, "No trickplay data")
		return
	}
	sess.mu.Lock()
	status, duration := sess.status, sess.duration
	sess.mu.Unlock()
	if status == nil || status.StreamPath == "" {
		writeError(w, http.StatusNotFound, "No trickplay data")
		return
	}
	cacheKey := status.StreamPath + "|" + strconv.Itoa(index)
	if tile, ok := s.tiles.get(cacheKey); ok {
		writeTile(w, tile)
		return
	}
	frames, done := s.deps.ThumbnailFrames(status.StreamPath)
	tile, complete, err := composeTile(frames, done, index, duration)
	if err != nil {
		writeError(w, http.StatusNotFound, "No trickplay data")
		return
	}
	if complete {
		s.tiles.put(cacheKey, tile)
	}
	writeTile(w, tile)
}

func writeTile(w http.ResponseWriter, tile []byte) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write(tile)
}

// composeTile renders tile index: slot j shows the frame nearest to the
// middle of [j*interval, (j+1)*interval). The tile is complete when every
// slot within the duration got a frame from finished generation.
func composeTile(frames []ThumbnailFrame, done bool, index int, duration float64) ([]byte, bool, error) {
	if len(frames) == 0 {
		return nil, false, fmt.Errorf("no thumbnails yet")
	}
	interval := float64(trickplayIntervalMS) / 1000
	perTile := trickplayTileSide * trickplayTileSide
	first := index * perTile
	if duration > 0 && float64(first)*interval >= duration {
		return nil, false, fmt.Errorf("tile beyond the end")
	}
	// Frames further from a slot than the generation spacing are not the
	// right picture yet; generation fills them in later.
	spacing := interval
	if len(frames) > 1 {
		spacing = math.Max(interval, (frames[len(frames)-1].TimeSec-frames[0].TimeSec)/float64(len(frames)-1))
	}
	canvas := image.NewRGBA(image.Rect(0, 0, trickplayWidth*trickplayTileSide, trickplayHeight*trickplayTileSide))
	decoded := map[string]image.Image{}
	complete, drawn := done, 0
	for slot := 0; slot < perTile; slot++ {
		at := (float64(first+slot) + 0.5) * interval
		if duration > 0 && at-interval/2 >= duration {
			break
		}
		frame, dist := nearestFrame(frames, at)
		if dist > spacing {
			complete = false
			continue
		}
		img, ok := decoded[frame.Path]
		if !ok {
			img = decodeJPEG(frame.Path)
			decoded[frame.Path] = img
		}
		if img == nil {
			complete = false
			continue
		}
		cell := image.Rect(0, 0, trickplayWidth, trickplayHeight).Add(image.Pt(slot%trickplayTileSide*trickplayWidth, slot/trickplayTileSide*trickplayHeight))
		xdraw.ApproxBiLinear.Scale(canvas, fitRect(cell, img.Bounds()), img, img.Bounds(), xdraw.Src, nil)
		drawn++
	}
	if drawn == 0 {
		return nil, false, fmt.Errorf("no frames for tile %d yet", index)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: 80}); err != nil {
		return nil, false, err
	}
	return buf.Bytes(), complete, nil
}

func nearestFrame(frames []ThumbnailFrame, at float64) (ThumbnailFrame, float64) {
	best, bestDist := frames[0], math.Inf(1)
	for _, f := range frames {
		if d := math.Abs(f.TimeSec - at); d < bestDist {
			best, bestDist = f, d
		}
	}
	return best, bestDist
}

func decodeJPEG(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		return nil
	}
	return img
}

// fitRect letterboxes src's aspect ratio into cell.
func fitRect(cell, src image.Rectangle) image.Rectangle {
	cw, ch := cell.Dx(), cell.Dy()
	sw, sh := src.Dx(), src.Dy()
	if sw <= 0 || sh <= 0 {
		return cell
	}
	w, h := cw, cw*sh/sw
	if h > ch {
		w, h = ch*sw/sh, ch
	}
	x, y := cell.Min.X+(cw-w)/2, cell.Min.Y+(ch-h)/2
	return image.Rect(x, y, x+w, y+h)
}

// tileCache keeps finished tiles, oldest out first.
type tileCache struct {
	mu    sync.Mutex
	tiles map[string][]byte
	order []string
	bytes int
}

func newTileCache() *tileCache { return &tileCache{tiles: map[string][]byte{}} }

func (c *tileCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tile, ok := c.tiles[key]
	return tile, ok
}

func (c *tileCache) put(key string, tile []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.tiles[key]; ok || len(tile) > trickplayCacheBytes {
		return
	}
	for c.bytes+len(tile) > trickplayCacheBytes && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		c.bytes -= len(c.tiles[oldest])
		delete(c.tiles, oldest)
	}
	c.tiles[key] = tile
	c.order = append(c.order, key)
	c.bytes += len(tile)
}
