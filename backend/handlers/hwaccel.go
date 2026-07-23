package handlers

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// HWAccelKind identifies a hardware-accelerated H.264 encode backend.
type HWAccelKind string

const (
	HWNone         HWAccelKind = "none"
	HWNVENC        HWAccelKind = "nvenc"
	HWQSV          HWAccelKind = "qsv"
	HWVAAPI        HWAccelKind = "vaapi"
	HWVideoToolbox HWAccelKind = "videotoolbox"
)

// vaapiDefaultDevice is the render node used for VAAPI/QSV when one exists.
// In Docker this requires `--device /dev/dri` (or equivalent) to be passed
// through; if the node is absent we fall back to CPU.
const vaapiDefaultDevice = "/dev/dri/renderD128"

// HWAccelCaps describes the hardware capabilities detected for a given ffmpeg
// binary plus the configured preference. It is computed once (the probe runs a
// real null test-encode) and reused for every transcode.
type HWAccelCaps struct {
	// Encode is the chosen H.264 encode backend (HWNone => CPU libx264).
	Encode HWAccelKind
	// EncodeDevice is the render node for VAAPI/QSV (empty otherwise).
	EncodeDevice string
	// Tonemap is the verified HDR/DV -> SDR tone-mapping implementation:
	//   "libplacebo" — GPU (Vulkan); the only path that correctly applies the
	//                  Dolby Vision RPU (incl. Profile 5), preferred when usable.
	//   "opencl"     — GPU (tonemap_opencl), vendor-neutral, HDR10/HLG only.
	//   "zscale"     — CPU (libzimg), reliable fallback, HDR10/HLG only.
	//   ""           — no tone mapping available (naive transcode).
	Tonemap string
}

// videoEncodePlan is the concrete set of ffmpeg arguments for one transcode,
// derived from HWAccelCaps plus whether tone mapping is required.
type videoEncodePlan struct {
	// GlobalArgs are options that must precede -i (device initialization).
	GlobalArgs []string
	// Filter is the -vf value ("" when no filtering is needed).
	Filter string
	// EncoderArgs are the output-side codec options (-c:v ...).
	EncoderArgs []string
	// Tonemapped reports whether the output was tone mapped to SDR. When true
	// the HLS playlist must advertise SDR rather than PQ.
	Tonemapped bool
	// HardwareEncode reports whether a GPU encoder was selected.
	HardwareEncode bool
	// Kind is the encode backend used (for logging).
	Kind HWAccelKind
}

// hwAccelOnce guards lazy detection on the manager.
func (m *HLSManager) hwAccelCaps() HWAccelCaps {
	m.hwAccelOnce.Do(func() {
		pref := "auto"
		if m.configManager != nil {
			if settings, err := m.configManager.Load(); err == nil {
				if p := strings.ToLower(strings.TrimSpace(settings.Transmux.HardwareAcceleration)); p != "" {
					pref = p
				}
			}
		}
		m.hwAccel = detectHWAccel(m.ffmpegPath, pref)
	})
	return m.hwAccel
}

// detectHWAccel probes the ffmpeg binary and host devices to pick the best
// working H.264 encode backend honoring the configured preference. "auto"
// tries each candidate in priority order and verifies it with a tiny null
// test-encode; the first that succeeds wins. Any explicit preference that
// fails verification falls back to CPU.
func detectHWAccel(ffmpegPath, pref string) HWAccelCaps {
	caps := HWAccelCaps{Encode: HWNone}
	if strings.TrimSpace(ffmpegPath) == "" {
		return caps
	}

	encoders := ffmpegEncoderSet(ffmpegPath)
	filters := ffmpegFilterSet(ffmpegPath)

	pref = strings.ToLower(strings.TrimSpace(pref))
	if pref == "" {
		pref = "auto"
	}

	// Pick the best tone-mapping implementation that actually works. GPU paths
	// require a runtime device (Vulkan/OpenCL) that may be absent even when the
	// filter is compiled in — so each is verified with a null filter-graph run.
	// libplacebo is preferred: it is the only filter that applies the Dolby
	// Vision RPU correctly (other paths mishandle DV Profile 5's IPT base layer).
	// An explicit "none" preference disables GPU tone mapping as well as GPU
	// encoding. CPU zscale remains available for HDR10/DV7/DV8 fallback.
	caps.Tonemap = detectTonemap(ffmpegPath, filters, pref != string(HWNone))

	if pref == string(HWNone) {
		return caps
	}

	var candidates []HWAccelKind
	switch pref {
	case "auto":
		// NVENC and QSV/VAAPI are the common Docker passthrough cases;
		// VideoToolbox covers macOS hosts.
		candidates = []HWAccelKind{HWNVENC, HWQSV, HWVAAPI}
		if runtime.GOOS == "darwin" {
			candidates = append([]HWAccelKind{HWVideoToolbox}, candidates...)
		}
	case string(HWNVENC):
		candidates = []HWAccelKind{HWNVENC}
	case string(HWQSV):
		candidates = []HWAccelKind{HWQSV}
	case string(HWVAAPI):
		candidates = []HWAccelKind{HWVAAPI}
	case string(HWVideoToolbox):
		candidates = []HWAccelKind{HWVideoToolbox}
	default:
		return caps
	}

	for _, kind := range candidates {
		device, ok := hwEncoderUsable(ffmpegPath, kind, encoders)
		if !ok {
			continue
		}
		caps.Encode = kind
		caps.EncodeDevice = device
		return caps
	}

	return caps
}

// detectTonemap returns the best verified tone-mapping implementation.
func detectTonemap(ffmpegPath string, filters map[string]bool, allowGPU bool) string {
	if allowGPU && filters["libplacebo"] && libplaceboUsable(ffmpegPath) {
		return "libplacebo"
	}
	if allowGPU && filters["tonemap_opencl"] && openclTonemapUsable(ffmpegPath) {
		return "opencl"
	}
	if filters["zscale"] {
		return "zscale"
	}
	return ""
}

// libplaceboUsable verifies a Vulkan device initializes and the libplacebo
// filter runs on a real GPU. Mesa's Lavapipe/llvmpipe software Vulkan device
// can pass a tiny functional probe but is far too slow for real-time 4K HLS.
func libplaceboUsable(ffmpegPath string) bool {
	if softwareVulkanForcedByEnvironment() {
		return false
	}

	ok, output := runFilterProbeWithOutput(ffmpegPath,
		[]string{"-init_hw_device", "vulkan=vk", "-filter_hw_device", "vk"},
		"color=c=black:s=128x128:d=0.1",
		"libplacebo=format=yuv420p",
		"verbose")
	return ok && !vulkanProbeUsesSoftwareRenderer(output)
}

// openclTonemapUsable verifies an OpenCL device initializes and tonemap_opencl runs.
func openclTonemapUsable(ffmpegPath string) bool {
	return runFilterProbe(ffmpegPath,
		[]string{"-init_hw_device", "opencl=ocl", "-filter_hw_device", "ocl"},
		"color=c=black:s=128x128:d=0.1,format=yuv420p10le,setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc",
		"format=p010le,hwupload,tonemap_opencl=tonemap=hable:t=bt709:m=bt709:p=bt709:format=nv12,hwdownload,format=nv12")
}

// runFilterProbe runs a tiny null-output transcode to confirm a filter graph
// (and any hardware device it needs) is usable on this host.
func runFilterProbe(ffmpegPath string, globalArgs []string, lavfiSrc, filter string) bool {
	ok, _ := runFilterProbeWithOutput(ffmpegPath, globalArgs, lavfiSrc, filter, "error")
	return ok
}

func runFilterProbeWithOutput(ffmpegPath string, globalArgs []string, lavfiSrc, filter, logLevel string) (bool, string) {
	if strings.TrimSpace(ffmpegPath) == "" {
		return false, ""
	}
	args := append([]string{"-hide_banner", "-loglevel", logLevel}, globalArgs...)
	args = append(args, "-f", "lavfi", "-i", lavfiSrc, "-vf", filter, "-frames:v", "1", "-f", "null", "-")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput()
	return err == nil, string(output)
}

func softwareVulkanForcedByEnvironment() bool {
	icd := strings.ToLower(os.Getenv("VK_ICD_FILENAMES"))
	if strings.Contains(icd, "lavapipe") || strings.Contains(icd, "lvp_icd") {
		return true
	}
	return strings.TrimSpace(os.Getenv("LIBGL_ALWAYS_SOFTWARE")) == "1"
}

func vulkanProbeUsesSoftwareRenderer(output string) bool {
	lower := strings.ToLower(output)
	for _, marker := range []string{
		"lavapipe",
		"llvmpipe",
		"software rasterizer",
		"device type: cpu",
		"device_type: cpu",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// hwEncoderUsable reports whether the given backend's H.264 encoder exists and
// passes a quick null test-encode (which confirms a working device — this is
// what makes Docker /dev passthrough detection reliable rather than assumed).
func hwEncoderUsable(ffmpegPath string, kind HWAccelKind, encoders map[string]bool) (device string, ok bool) {
	encoder := hwEncoderName(kind)
	if encoder == "" || !encoders[encoder] {
		return "", false
	}

	switch kind {
	case HWVAAPI, HWQSV:
		// Require the render node to exist before even attempting the probe.
		if _, err := os.Stat(vaapiDefaultDevice); err != nil {
			return "", false
		}
		device = vaapiDefaultDevice
	}

	args := []string{"-hide_banner", "-loglevel", "error"}
	switch kind {
	case HWVAAPI:
		args = append(args, "-vaapi_device", device,
			"-f", "lavfi", "-i", "color=c=black:s=128x128:d=0.1",
			"-vf", "format=nv12,hwupload", "-c:v", encoder)
	case HWQSV:
		args = append(args, "-init_hw_device", "qsv=hw:"+device, "-filter_hw_device", "hw",
			"-f", "lavfi", "-i", "color=c=black:s=128x128:d=0.1",
			"-vf", "format=nv12,hwupload=extra_hw_frames=16,format=qsv", "-c:v", encoder)
	default:
		args = append(args, "-f", "lavfi", "-i", "color=c=black:s=128x128:d=0.1",
			"-c:v", encoder)
	}
	args = append(args, "-frames:v", "1", "-f", "null", "-")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	if err := cmd.Run(); err != nil {
		return "", false
	}
	return device, true
}

func hwEncoderName(kind HWAccelKind) string {
	switch kind {
	case HWNVENC:
		return "h264_nvenc"
	case HWQSV:
		return "h264_qsv"
	case HWVAAPI:
		return "h264_vaapi"
	case HWVideoToolbox:
		return "h264_videotoolbox"
	default:
		return ""
	}
}

// ffmpegEncoderSet returns the set of encoder names reported by ffmpeg.
func ffmpegEncoderSet(ffmpegPath string) map[string]bool {
	return ffmpegTokenSet(ffmpegPath, "-encoders")
}

// ffmpegFilterSet returns the set of filter names reported by ffmpeg.
func ffmpegFilterSet(ffmpegPath string) map[string]bool {
	return ffmpegTokenSet(ffmpegPath, "-filters")
}

// ffmpegTokenSet runs `ffmpeg -hide_banner <listFlag>` and extracts the second
// whitespace-separated token of each capability line (the encoder/filter name).
func ffmpegTokenSet(ffmpegPath, listFlag string) map[string]bool {
	set := make(map[string]bool)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", listFlag)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return set
	}
	for _, line := range strings.Split(out.String(), "\n") {
		fields := strings.Fields(line)
		// Capability lines look like " V..... h264_nvenc  NVIDIA NVENC ...".
		// The first field is the flags column, the second is the name.
		if len(fields) < 2 {
			continue
		}
		flags := fields[0]
		// Skip header/separator lines (flags column is letters/dots only).
		if !looksLikeCapabilityFlags(flags) {
			continue
		}
		set[fields[1]] = true
	}
	return set
}

func looksLikeCapabilityFlags(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r == '.' {
			continue
		}
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			continue
		}
		return false
	}
	return true
}

// buildVideoEncodePlan assembles the ffmpeg arguments for a web transcode given
// the detected capabilities and whether the source is HDR/DV (tonemapNeeded).
func buildVideoEncodePlan(caps HWAccelCaps, tonemapNeeded bool) videoEncodePlan {
	plan := videoEncodePlan{Kind: caps.Encode}

	// VAAPI/QSV encoders need their own filter hardware device for the hwupload
	// step, which conflicts with a second (Vulkan/OpenCL) device for GPU tone
	// mapping. To stay robust we tone map on the CPU (zscale) for those encoders
	// and reserve the GPU tone-map filters for encoders that consume system
	// memory frames (NVENC / VideoToolbox / libx264).
	tonemapImpl := caps.Tonemap
	if caps.Encode == HWVAAPI || caps.Encode == HWQSV {
		if caps.Tonemap == "" {
			tonemapImpl = ""
		} else {
			tonemapImpl = "zscale"
		}
	}

	var filters []string
	if tonemapNeeded && tonemapImpl != "" {
		plan.Tonemapped = true
		switch tonemapImpl {
		case "libplacebo":
			// GPU tone mapping via libplacebo (Vulkan). Applies the Dolby Vision
			// RPU when present; output is downloaded to system-memory yuv420p.
			plan.GlobalArgs = append(plan.GlobalArgs, "-init_hw_device", "vulkan=vk", "-filter_hw_device", "vk")
			filters = append(filters,
				"libplacebo=tonemapping=bt.2390:colorspace=bt709:color_primaries=bt709:color_trc=bt709:range=tv:apply_dolbyvision=true:format=yuv420p")
		case "opencl":
			// GPU tone mapping via OpenCL (vendor-neutral, HDR10/HLG). Output is
			// downloaded back to system memory so the encoder stage is independent.
			plan.GlobalArgs = append(plan.GlobalArgs, "-init_hw_device", "opencl=ocl", "-filter_hw_device", "ocl")
			filters = append(filters,
				"setparams=range=tv:color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc",
				"format=p010le",
				"hwupload",
				"tonemap_opencl=tonemap=hable:desat=0:t=bt709:m=bt709:p=bt709:format=nv12",
				"hwdownload",
				"format=nv12")
		default: // zscale (CPU, libzimg). Hable operator, 100-nit ref, BT.709 SDR.
			filters = append(filters,
				// Some DV8 files omit container-level color metadata even though
				// their base layer is HDR10. Without explicit input properties,
				// zscale aborts with "no path between colorspaces".
				"setparams=range=tv:color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc",
				"zscale=t=linear:npl=100",
				"format=gbrpf32le",
				"zscale=p=bt709",
				"tonemap=tonemap=hable:desat=0",
				"zscale=t=bt709:m=bt709:r=tv",
				"format=yuv420p")
		}
	}

	// Encoder stage. VAAPI/QSV need their frames uploaded to the device.
	switch caps.Encode {
	case HWNVENC:
		plan.HardwareEncode = true
		if len(filters) == 0 {
			filters = append(filters, "format=yuv420p")
		}
		plan.EncoderArgs = []string{
			"-c:v", "h264_nvenc",
			"-preset", "p4",
			"-tune", "ll",
			"-rc", "vbr",
			"-cq", "23",
			"-profile:v", "high",
			"-pix_fmt", "yuv420p",
		}
	case HWVideoToolbox:
		plan.HardwareEncode = true
		if len(filters) == 0 {
			filters = append(filters, "format=yuv420p")
		}
		plan.EncoderArgs = []string{
			"-c:v", "h264_videotoolbox",
			"-realtime", "1",
			"-profile:v", "high",
			"-q:v", "60",
			"-pix_fmt", "yuv420p",
		}
	case HWVAAPI:
		plan.HardwareEncode = true
		plan.GlobalArgs = append(plan.GlobalArgs, "-vaapi_device", caps.EncodeDevice)
		filters = append(filters, "format=nv12", "hwupload")
		plan.EncoderArgs = []string{
			"-c:v", "h264_vaapi",
			"-rc_mode", "CQP",
			"-qp", "23",
			"-profile:v", "high",
		}
	case HWQSV:
		plan.HardwareEncode = true
		plan.GlobalArgs = append(plan.GlobalArgs, "-init_hw_device", "qsv=hw:"+caps.EncodeDevice, "-filter_hw_device", "hw")
		filters = append(filters, "format=nv12", "hwupload=extra_hw_frames=64", "format=qsv")
		plan.EncoderArgs = []string{
			"-c:v", "h264_qsv",
			"-preset", "veryfast",
			"-global_quality", "23",
			"-profile:v", "high",
		}
	default: // CPU libx264
		if len(filters) == 0 {
			filters = append(filters, "format=yuv420p")
		}
		plan.EncoderArgs = []string{
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-crf", "23",
			"-profile:v", "high",
			"-level", "4.1",
			"-pix_fmt", "yuv420p",
		}
	}

	if len(filters) > 0 {
		plan.Filter = strings.Join(filters, ",")
	}
	return plan
}
