package jellyfinbridge

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
)

func writeTestFrames(t *testing.T, times []float64) []ThumbnailFrame {
	t.Helper()
	dir := t.TempDir()
	frames := make([]ThumbnailFrame, 0, len(times))
	for i, at := range times {
		img := image.NewRGBA(image.Rect(0, 0, 640, 360))
		for y := 0; y < 360; y++ {
			for x := 0; x < 640; x++ {
				img.Set(x, y, color.RGBA{R: uint8(i * 20), G: 100, B: 200, A: 255})
			}
		}
		path := filepath.Join(dir, fmt.Sprintf("thumb_%d.jpg", i))
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, nil); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, ThumbnailFrame{TimeSec: at, Path: path})
	}
	return frames
}

func TestTrickplayFromSeekThumbnails(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	installFakePlayback(t, h) // 120 s stream
	enabled := true
	frames := writeTestFrames(t, []float64{15, 45, 75, 105})
	done := false
	var thumbStarts atomic.Int32
	h.srv.deps.ThumbnailsEnabled = func() bool { return enabled }
	h.srv.deps.ThumbnailFrames = func(path string) ([]ThumbnailFrame, bool) {
		if path != "/webdav/movie.mkv" {
			t.Errorf("frames path = %q", path)
		}
		return frames, done
	}
	h.srv.deps.Handlers.ThumbnailsStart = func(w http.ResponseWriter, r *http.Request) {
		thumbStarts.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}
	tok := h.login("Living Room", "alice-pass")
	itemID := movieID(t, h, tok)

	// The item carries trickplay info keyed by the media source PlaybackInfo
	// will return.
	item := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+itemID, tok, nil))
	if len(item.Trickplay) != 2 || item.Trickplay[itemID] == nil { // the device's media source and the item (automatic version)
		t.Fatalf("item trickplay = %+v", item.Trickplay)
	}
	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+itemID+"/PlaybackInfo", tok, map[string]any{}))
	msid := info.MediaSources[0].ID
	res, ok := item.Trickplay[msid]["320"]
	if !ok || res.Interval != trickplayIntervalMS || res.TileWidth != 10 || res.ThumbnailCount <= 0 {
		t.Fatalf("trickplay for %s = %+v", msid, item.Trickplay)
	}
	waitFor(t, func() bool { return thumbStarts.Load() == 1 })

	tile := h.do(http.MethodGet, "/Videos/"+itemID+"/Trickplay/320/0.jpg?MediaSourceId="+msid+"&ApiKey="+tok, "", nil)
	if tile.Code != http.StatusOK || tile.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("tile = %d %s", tile.Code, tile.Body.String())
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(tile.Body.Bytes()))
	if err != nil || cfg.Width != 3200 || cfg.Height != 1800 {
		t.Fatalf("tile image = %+v %v", cfg, err)
	}
	// Unfinished tiles are not cached; finished ones are.
	if _, cached := h.srv.tiles.get("/webdav/movie.mkv|0"); cached {
		t.Fatal("tile cached before generation finished")
	}
	done = true
	h.do(http.MethodGet, "/Videos/"+itemID+"/Trickplay/320/0.jpg?MediaSourceId="+msid+"&ApiKey="+tok, "", nil)
	if _, cached := h.srv.tiles.get("/webdav/movie.mkv|0"); !cached {
		t.Fatal("finished tile not cached")
	}

	// The media source ID is the capability; a wrong one or a tile past the
	// end finds nothing.
	if rec := h.do(http.MethodGet, "/Videos/"+itemID+"/Trickplay/320/0.jpg?MediaSourceId=0123456789abcdef0123456789abcdef", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown media source = %d", rec.Code)
	}
	if rec := h.do(http.MethodGet, "/Videos/"+itemID+"/Trickplay/320/"+strconv.Itoa(5)+".jpg?MediaSourceId="+msid+"&ApiKey="+tok, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("tile past the end = %d", rec.Code)
	}

	enabled = false
	if item := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+itemID, tok, nil)); item.Trickplay != nil {
		t.Fatal("trickplay advertised with thumbnails disabled")
	}
}

func TestComposeTileMarksMissingSlots(t *testing.T) {
	frames := writeTestFrames(t, []float64{15})
	// A 10-minute title with one frame: most slots have no frame near them.
	if _, complete, err := composeTile(frames, true, 0, 600); err != nil || complete {
		t.Fatalf("sparse tile complete=%v err=%v", complete, err)
	}
	if _, _, err := composeTile(nil, false, 0, 600); err == nil {
		t.Fatal("tile composed without frames")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
