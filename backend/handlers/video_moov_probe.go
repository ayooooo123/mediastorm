package handlers

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"novastream/services/streaming"
)

// providerResponseTotalSize extracts the total resource size from a provider response.
func providerResponseTotalSize(resp *streaming.Response) int64 {
	if resp == nil {
		return 0
	}
	if resp.Headers != nil {
		if cr := resp.Headers.Get("Content-Range"); cr != "" {
			if total := externalResponseTotalSize(cr, ""); total > 0 {
				return total
			}
		}
	}
	if resp.ContentLength > 0 && resp.Status != http.StatusPartialContent {
		return resp.ContentLength
	}
	if resp.Headers != nil {
		if cl := resp.Headers.Get("Content-Length"); cl != "" {
			if parsed, err := strconv.ParseInt(cl, 10, 64); err == nil && parsed > 0 && resp.Status != http.StatusPartialContent {
				return parsed
			}
		}
	}
	return 0
}

// mp4MoovAtEndOffset reports the byte offset of a moov box when the leading
// sample is ftyp + mdat (classic non-faststart progressive MP4).
func mp4MoovAtEndOffset(prefix []byte, totalSize int64) (int64, bool) {
	if totalSize <= 0 || len(prefix) < 16 {
		return 0, false
	}
	off := 0
	sawFtyp := false
	for off+8 <= len(prefix) {
		size, typ, hdr, ok := parseMP4BoxHeader(prefix, off)
		if !ok || size < int64(hdr) {
			return 0, false
		}
		boxEnd := int64(off) + size
		switch typ {
		case "ftyp":
			sawFtyp = true
		case "moov":
			return 0, false // already at front
		case "mdat":
			if !sawFtyp {
				return 0, false
			}
			if boxEnd > 0 && boxEnd < totalSize {
				return boxEnd, true
			}
			return 0, false
		}
		if boxEnd > int64(len(prefix)) {
			return 0, false
		}
		off = int(boxEnd)
	}
	return 0, false
}

func parseMP4BoxHeader(data []byte, offset int) (size int64, typ string, hdr int, ok bool) {
	if offset < 0 || offset+8 > len(data) {
		return 0, "", 0, false
	}
	size32 := binary.BigEndian.Uint32(data[offset : offset+4])
	typ = string(data[offset+4 : offset+8])
	hdr = 8
	switch size32 {
	case 0:
		return 0, typ, hdr, false
	case 1:
		if offset+16 > len(data) {
			return 0, "", 0, false
		}
		size = int64(binary.BigEndian.Uint64(data[offset+8 : offset+16]))
		hdr = 16
	default:
		size = int64(size32)
	}
	if size < int64(hdr) {
		return 0, "", 0, false
	}
	return size, typ, hdr, true
}

func extractMP4Box(data []byte, want string) []byte {
	off := 0
	for off+8 <= len(data) {
		size, typ, hdr, ok := parseMP4BoxHeader(data, off)
		if !ok {
			return nil
		}
		if typ == want {
			end := off + int(size)
			if end > len(data) {
				if typ == "ftyp" && off+hdr <= len(data) {
					return data[off:]
				}
				return nil
			}
			return data[off:end]
		}
		if int64(off)+size > int64(len(data)) {
			return nil
		}
		off += int(size)
	}
	return nil
}

func (h *VideoHandler) readProviderRange(ctx context.Context, cleanPath string, start, end int64) ([]byte, error) {
	if h.streamer == nil {
		return nil, fmt.Errorf("stream provider not configured")
	}
	if end < start {
		return nil, fmt.Errorf("invalid range %d-%d", start, end)
	}
	if end-start+1 > providerProbeSampleBytes {
		end = start + providerProbeSampleBytes - 1
	}
	resp, err := h.streamer.Stream(ctx, streaming.Request{
		Path:        cleanPath,
		Method:      http.MethodGet,
		RangeHeader: fmt.Sprintf("bytes=%d-%d", start, end),
	})
	if err != nil {
		return nil, err
	}
	defer resp.Close()
	if resp.Body == nil {
		return nil, fmt.Errorf("empty body for range %d-%d of %q", start, end, cleanPath)
	}
	want := end - start + 1
	buf := make([]byte, want)
	n, err := io.ReadFull(resp.Body, buf)
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func (h *VideoHandler) runFFProbeFileInner(ctx context.Context, path string, probesize, analyzeduration int) (*ffprobeOutput, error) {
	if h.ffprobePath == "" {
		return nil, errors.New("ffprobe not configured")
	}
	args := []string{
		"-v", "error",
		"-probesize", strconv.Itoa(probesize),
		"-analyzeduration", strconv.Itoa(analyzeduration),
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		"-i", path,
	}
	cmd := exec.CommandContext(ctx, h.ffprobePath, args...)
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("ffprobe timeout")
		}
		errMsg := strings.TrimSpace(string(stderr))
		if errMsg != "" {
			return nil, fmt.Errorf("ffprobe error: %s", errMsg)
		}
		return nil, err
	}
	var parsed ffprobeOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}
	return &parsed, nil
}

func (h *VideoHandler) ffprobeFtypMoov(ctx context.Context, prefix, moov []byte, originalTotalSize int64) (*ffprobeOutput, error) {
	ftyp := extractMP4Box(prefix, "ftyp")
	if len(ftyp) == 0 {
		ftyp = []byte{
			0x00, 0x00, 0x00, 0x14, 'f', 't', 'y', 'p',
			'i', 's', 'o', 'm', 0x00, 0x00, 0x02, 0x00,
			'i', 's', 'o', 'm',
		}
	}
	tmp, err := os.CreateTemp("", "novastream-probe-*.mp4")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(ftyp); err != nil {
		tmp.Close()
		return nil, err
	}
	if _, err := tmp.Write(moov); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	meta, err := h.runFFProbeFileInner(ctx, tmpPath, 12*1024*1024, 5_000_000)
	if err != nil {
		return nil, err
	}
	// Temp sample is ftyp+moov only — restore real media size for clients.
	if meta != nil && originalTotalSize > 0 {
		meta.Format.Size = strconv.FormatInt(originalTotalSize, 10)
		if dur := parseFloat(meta.Format.Duration); dur > 0 {
			meta.Format.BitRate = strconv.FormatInt(int64(float64(originalTotalSize*8)/dur), 10)
		}
	}
	return meta, nil
}

// runFFProbeProviderMoovAtEnd builds a tiny ftyp+moov sample via ranged provider
// reads and probes it. Playback remains a direct range proxy of the original file.
func (h *VideoHandler) runFFProbeProviderMoovAtEnd(ctx context.Context, cleanPath string, prefix []byte, moovOffset, totalSize int64) (*ffprobeOutput, error) {
	if h.streamer == nil {
		return nil, fmt.Errorf("stream provider not configured")
	}
	if totalSize <= 0 {
		head, err := h.streamer.Stream(ctx, streaming.Request{Path: cleanPath, Method: http.MethodHead})
		if err != nil {
			return nil, err
		}
		totalSize = providerResponseTotalSize(head)
		_ = head.Close()
		if totalSize <= 0 {
			return nil, fmt.Errorf("moov-at-end probe: unknown total size for %q", cleanPath)
		}
	}

	if moovOffset <= 0 {
		if off, ok := mp4MoovAtEndOffset(prefix, totalSize); ok {
			moovOffset = off
		} else {
			start := totalSize - providerProbeSampleBytes
			if start < 0 {
				start = 0
			}
			tail, err := h.readProviderRange(ctx, cleanPath, start, totalSize-1)
			if err != nil {
				return nil, err
			}
			for i := 0; i+8 <= len(tail); i++ {
				size, typ, _, ok := parseMP4BoxHeader(tail, i)
				if !ok {
					continue
				}
				if typ == "moov" {
					moovOffset = start + int64(i)
					need := size
					have := int64(len(tail) - i)
					if have < need {
						full, ferr := h.readProviderRange(ctx, cleanPath, moovOffset, moovOffset+need-1)
						if ferr != nil {
							return nil, ferr
						}
						return h.ffprobeFtypMoov(ctx, prefix, full, totalSize)
					}
					return h.ffprobeFtypMoov(ctx, prefix, tail[i:i+int(need)], totalSize)
				}
				if size > 1 {
					i += int(size) - 1
				}
			}
			return nil, fmt.Errorf("moov-at-end probe: moov not found near end of %q", cleanPath)
		}
	}

	header, err := h.readProviderRange(ctx, cleanPath, moovOffset, moovOffset+15)
	if err != nil {
		return nil, err
	}
	size, typ, _, ok := parseMP4BoxHeader(header, 0)
	if !ok || typ != "moov" {
		return nil, fmt.Errorf("moov-at-end probe: expected moov at %d for %q, got %q", moovOffset, cleanPath, typ)
	}
	moov, err := h.readProviderRange(ctx, cleanPath, moovOffset, moovOffset+size-1)
	if err != nil {
		return nil, err
	}
	log.Printf("[video] moov-at-end probe for %q: moovOffset=%d moovSize=%d total=%d", cleanPath, moovOffset, size, totalSize)
	return h.ffprobeFtypMoov(ctx, prefix, moov, totalSize)
}
