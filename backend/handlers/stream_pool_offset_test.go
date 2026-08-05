package handlers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/services/streaming"
)

// shiftingProvider serves the file from a different offset than the one asked
// for, the way the debrid layer can after RAR-offset rewriting or parallel-fetch
// realignment, and reports that shifted window honestly in Content-Range.
type shiftingProvider struct {
	data  []byte
	shift int64
}

func (p *shiftingProvider) Stream(_ context.Context, req streaming.Request) (*streaming.Response, error) {
	start, _, ok := parseByteRange(req.RangeHeader)
	if !ok {
		start = 0
	}
	served := start + p.shift
	if served > int64(len(p.data)) {
		served = int64(len(p.data))
	}
	total := int64(len(p.data))
	headers := http.Header{}
	headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", served, total-1, total))
	headers.Set("Accept-Ranges", "bytes")
	return &streaming.Response{
		Status:        http.StatusPartialContent,
		Headers:       headers,
		ContentLength: total - served,
		Body:          io.NopCloser(bytes.NewReader(p.data[served:])),
		Filename:      "movie.mkv",
	}, nil
}

// The pool used to record startByte as the offset it requested, so a shifted
// upstream window left every reader of the slot receiving bytes from further
// into the file while the response still advertised the requested range. A
// renderer reading that stream sees a malformed container.
func TestStreamPoolNeverServesBytesFromAShiftedWindow(t *testing.T) {
	const (
		path   = "/debrid/torbox/900/file/0/movie.mkv"
		reqPos = int64(4 << 20)
	)
	data := spoolTestPayload(24 << 20)
	// A one MiB forward shift, matching what was observed in production.
	provider := &shiftingProvider{data: data, shift: 1 << 20}
	pool := newStreamPool(nil)
	defer pool.close()

	req := httptest.NewRequest(http.MethodGet, "/api/video/stream", nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", reqPos, reqPos+4095))
	rec := httptest.NewRecorder()

	served, err := pool.serve(
		rec, req, path, reqPos, provider,
		func(w http.ResponseWriter) { w.Header().Set("Accept-Ranges", "bytes") },
		"", "",
	)

	// Refusing to pool is the correct outcome: the caller then falls back to a
	// direct stream. What must never happen is a 206 whose body is the shifted
	// window while its Content-Range claims the requested offset.
	if !served {
		return
	}
	if err != nil {
		return
	}
	body := rec.Body.Bytes()
	if len(body) == 0 {
		return
	}
	want := data[reqPos : reqPos+int64(len(body))]
	if !bytes.Equal(body, want) {
		shifted := data[reqPos+provider.shift : reqPos+provider.shift+int64(len(body))]
		if bytes.Equal(body, shifted) {
			t.Fatalf("pool served the shifted window: requested %d but returned bytes from %d",
				reqPos, reqPos+provider.shift)
		}
		t.Fatalf("pool served bytes matching neither the requested nor the shifted window at %d", reqPos)
	}
}

// An upstream that honours the window must still be pooled and served correctly,
// so the guard does not disable the pool.
func TestStreamPoolServesHonouredWindowCorrectly(t *testing.T) {
	const (
		path   = "/debrid/torbox/901/file/0/movie.mkv"
		reqPos = int64(4 << 20)
		length = int64(4096)
	)
	data := spoolTestPayload(24 << 20)
	provider := &shiftingProvider{data: data, shift: 0}
	pool := newStreamPool(nil)
	defer pool.close()

	req := httptest.NewRequest(http.MethodGet, "/api/video/stream", nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", reqPos, reqPos+length-1))
	rec := httptest.NewRecorder()

	served, err := pool.serve(
		rec, req, path, reqPos, provider,
		func(w http.ResponseWriter) { w.Header().Set("Accept-Ranges", "bytes") },
		"", "",
	)
	if err != nil {
		t.Fatalf("serve returned error: %v", err)
	}
	if !served {
		t.Fatal("pool refused a window the upstream honoured")
	}
	body := rec.Body.Bytes()
	if int64(len(body)) != length {
		t.Fatalf("served %d bytes, want %d", len(body), length)
	}
	if !bytes.Equal(body, data[reqPos:reqPos+length]) {
		t.Fatal("pool served the wrong bytes for an honoured window")
	}
}

func spoolTestPayload(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251)
	}
	return data
}
