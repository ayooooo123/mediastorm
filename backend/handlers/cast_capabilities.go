package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"novastream/services/castcaps"
)

// CastCapabilitiesHandler owns receiver capability discovery: the probe assets
// a receiver plays, the cached verdicts, and the endpoints that expose them.
//
// Probing loads a four-second test stream on a receiver, so it never runs while
// starting a real cast. Results are cached per receiver and consulted from
// memory when a session is created, which keeps time-to-cast unchanged.
type CastCapabilitiesHandler struct {
	store      *castcaps.Store
	assetDir   string
	ffmpegPath string
	serverPort int

	assetsOnce sync.Once
	assetsErr  error
}

// probeVariantSpecs describes how to build each probe asset. They are tiny
// synthetic clips: no library content is needed and the whole set is ~1 MB.
var probeVariantSpecs = map[castcaps.Variant][]string{
	castcaps.VariantTSAACStereo: {
		"-c:v", "libx264", "-preset", "veryfast", "-profile:v", "high", "-level", "4.0", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-ac", "2", "-b:a", "128k", "-profile:a", "aac_low",
		"-hls_segment_type", "mpegts",
	},
	castcaps.VariantFMP4: {
		"-c:v", "libx264", "-preset", "veryfast", "-profile:v", "high", "-level", "4.0", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-ac", "2", "-b:a", "128k", "-profile:a", "aac_low",
		"-hls_segment_type", "fmp4", "-hls_fmp4_init_filename", "init.mp4",
	},
	// Real HEVC video: the only honest way to learn whether a receiver decodes
	// it. Kept at the same tiny resolution as the rest so the encode is cheap.
	castcaps.VariantHEVCFMP4: {
		"-c:v", "libx265", "-preset", "ultrafast", "-tag:v", "hvc1", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-ac", "2", "-b:a", "128k", "-profile:a", "aac_low",
		"-hls_segment_type", "fmp4", "-hls_fmp4_init_filename", "init.mp4",
	},
	castcaps.VariantTSAC3: {
		"-c:v", "libx264", "-preset", "veryfast", "-profile:v", "high", "-level", "4.0", "-pix_fmt", "yuv420p",
		"-c:a", "ac3", "-ac", "6", "-b:a", "384k",
		"-hls_segment_type", "mpegts",
	},
	castcaps.VariantTSAACMultichannel: {
		"-c:v", "libx264", "-preset", "veryfast", "-profile:v", "high", "-level", "4.0", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-ac", "6", "-b:a", "256k",
		"-hls_segment_type", "mpegts",
	},
}

func NewCastCapabilitiesHandler(cacheDir, assetDir, ffmpegPath string, serverPort int) *CastCapabilitiesHandler {
	handler := &CastCapabilitiesHandler{
		store:      castcaps.NewStore(cacheDir),
		assetDir:   assetDir,
		ffmpegPath: ffmpegPath,
		serverPort: serverPort,
	}
	handler.store.URLForVariant = handler.variantURL
	return handler
}

// Store exposes the cache so session creation can consult it without a probe.
func (h *CastCapabilitiesHandler) Store() *castcaps.Store {
	if h == nil {
		return nil
	}
	return h.store
}

// variantURL is what the receiver fetches. It must be a LAN address: the
// receiver is a separate device and cannot reach loopback.
func (h *CastCapabilitiesHandler) variantURL(variant castcaps.Variant) string {
	return fmt.Sprintf("http://%s:%d/cast/probe/%s/stream.m3u8", localLANAddress(), h.serverPort, variant)
}

// ensureAssets generates the probe streams once per process.
func (h *CastCapabilitiesHandler) ensureAssets() error {
	h.assetsOnce.Do(func() {
		h.assetsErr = h.buildAssets()
	})
	return h.assetsErr
}

func (h *CastCapabilitiesHandler) buildAssets() error {
	ffmpegPath := strings.TrimSpace(h.ffmpegPath)
	if ffmpegPath == "" {
		return fmt.Errorf("ffmpeg is not configured")
	}
	for variant, codecArgs := range probeVariantSpecs {
		dir := filepath.Join(h.assetDir, string(variant))
		playlist := filepath.Join(dir, "stream.m3u8")
		if _, err := os.Stat(playlist); err == nil {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create probe dir: %w", err)
		}

		segmentExt := ".ts"
		if variant == castcaps.VariantFMP4 {
			segmentExt = ".m4s"
		}
		args := []string{
			"-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24:duration=4",
			"-f", "lavfi", "-i", "sine=frequency=440:duration=4",
		}
		args = append(args, codecArgs...)
		args = append(args,
			"-f", "hls",
			"-hls_time", "2",
			"-hls_list_size", "0",
			"-hls_playlist_type", "vod",
			"-hls_flags", "independent_segments",
			"-hls_segment_filename", filepath.Join(dir, "segment%d"+segmentExt),
			playlist,
		)

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		output, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput() // #nosec G204 - fixed arg set
		cancel()
		if err != nil {
			return fmt.Errorf("build %s probe asset: %w (%s)", variant, err, strings.TrimSpace(string(output)))
		}
		log.Printf("[castcaps] built probe asset %s", variant)
	}
	return nil
}

// ServeProbeAsset serves the generated probe streams. This route is
// unauthenticated on purpose: the receiver has no session token, and the only
// thing exposed is a four-second synthetic test pattern.
func (h *CastCapabilitiesHandler) ServeProbeAsset(w http.ResponseWriter, r *http.Request) {
	if err := h.ensureAssets(); err != nil {
		http.Error(w, "probe assets unavailable", http.StatusServiceUnavailable)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/cast/probe/")
	clean := filepath.Clean("/" + rel)
	path := filepath.Join(h.assetDir, clean)
	if !strings.HasPrefix(path, filepath.Clean(h.assetDir)+string(os.PathSeparator)) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	switch filepath.Ext(path) {
	case ".m3u8":
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	case ".ts":
		w.Header().Set("Content-Type", "video/mp2t")
	case ".m4s", ".mp4":
		w.Header().Set("Content-Type", "video/mp4")
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	http.ServeFile(w, r, path)
}

// ListReceivers reports every receiver on the LAN with whatever is known about
// it. Discovery is a bounded port sweep, so this is safe to call from the UI.
func (h *CastCapabilitiesHandler) ListReceivers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	cidr := strings.TrimSpace(r.URL.Query().Get("cidr"))
	if cidr == "" {
		cidr = castcaps.LocalCIDR()
	}

	type receiverView struct {
		castcaps.Device
		Capabilities *castcaps.Capabilities `json:"capabilities,omitempty"`
	}
	devices := castcaps.Discover(ctx, cidr)
	views := make([]receiverView, 0, len(devices))
	for _, device := range devices {
		views = append(views, receiverView{Device: device, Capabilities: h.store.Lookup(device.Host)})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"cidr": cidr, "receivers": views})
}

// ProbeReceiver runs the capability matrix against one receiver on demand.
// It takes over that receiver for roughly ten seconds, so callers should only
// invoke it while nothing is playing on it.
func (h *CastCapabilitiesHandler) ProbeReceiver(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host == "" {
		http.Error(w, "missing host parameter", http.StatusBadRequest)
		return
	}
	if net.ParseIP(host) == nil {
		http.Error(w, "host must be an IP address", http.StatusBadRequest)
		return
	}
	if err := h.ensureAssets(); err != nil {
		http.Error(w, fmt.Sprintf("probe assets unavailable: %v", err), http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	device, err := castcaps.Describe(ctx, host)
	if err != nil {
		log.Printf("[castcaps] could not describe %s: %v", host, err)
		device = castcaps.Device{Host: host, Name: host}
	}
	caps, err := h.store.Probe(ctx, device)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(caps)
}

// localLANAddress is the address a receiver can reach this server on.
func localLANAddress() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if ipv4 := ipNet.IP.To4(); ipv4 != nil {
			return ipv4.String()
		}
	}
	return "127.0.0.1"
}
