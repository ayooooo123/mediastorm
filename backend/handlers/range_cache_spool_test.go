package handlers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"novastream/services/streaming"
)

// rangeIgnoringProvider serves deterministic bytes and, on its first call only,
// behaves like a debrid CDN that ignores Range and restarts the file from byte 0.
type rangeIgnoringProvider struct {
	data        []byte
	ignoreFirst bool

	mu    sync.Mutex
	calls int
}

func (p *rangeIgnoringProvider) Stream(_ context.Context, req streaming.Request) (*streaming.Response, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()

	total := int64(len(p.data))

	if call == 1 && p.ignoreFirst {
		// Range ignored: a full 200 body starting at byte 0.
		return &streaming.Response{
			Status:        http.StatusOK,
			Headers:       http.Header{},
			ContentLength: total,
			Body:          io.NopCloser(bytes.NewReader(p.data)),
		}, nil
	}

	start, end, ok := parseByteRange(req.RangeHeader)
	if !ok {
		return &streaming.Response{
			Status:        http.StatusOK,
			Headers:       http.Header{},
			ContentLength: total,
			Body:          io.NopCloser(bytes.NewReader(p.data)),
		}, nil
	}
	if end >= total {
		end = total - 1
	}
	headers := http.Header{}
	headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
	headers.Set("Accept-Ranges", "bytes")
	return &streaming.Response{
		Status:        http.StatusPartialContent,
		Headers:       headers,
		ContentLength: end - start + 1,
		Body:          io.NopCloser(bytes.NewReader(p.data[start : end+1])),
	}, nil
}

func spoolTestPayload(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251)
	}
	return data
}

// The startup spool asks for a 2 MiB window and files the body under that
// offset. When the upstream ignores Range and answers from byte 0, caching that
// body under a mid-file offset makes every later read of the block return data
// from the wrong place — the renderer sees a malformed container and reports
// "file cannot be recognized", while large sequential readers skip the spool
// entirely and play fine.
func TestSpoolDoesNotServeBytesFromAnIgnoredRange(t *testing.T) {
	const (
		path      = "/debrid/torbox/123/file/0/movie.mkv"
		wantStart = int64(3_000_000)
		wantLen   = int64(256)
	)
	data := spoolTestPayload(8 << 20)
	provider := &rangeIgnoringProvider{data: data, ignoreFirst: true}
	pool := newStreamPool(nil)
	defer pool.close()
	handler := &VideoHandler{streamer: provider, streamPool: pool}

	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/video/stream", nil)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", wantStart, wantStart+wantLen-1))
		rec := httptest.NewRecorder()
		if _, err := handler.streamViaProvider(rec, req, path); err != nil {
			t.Fatalf("streamViaProvider: %v", err)
		}
		return rec
	}

	// First attempt: the upstream ignores the window, so nothing may be cached.
	request()

	// Second attempt: the upstream honours the range. The bytes handed back must
	// be the bytes actually requested, not whatever a poisoned block held.
	rec := request()
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusPartialContent)
	}
	got := rec.Body.Bytes()
	want := data[wantStart : wantStart+wantLen]
	if len(got) != len(want) {
		t.Fatalf("served %d bytes, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d of the served range = %d, want %d (offset %d served data from the wrong place)",
				i, got[i], want[i], wantStart+int64(i))
		}
	}
	if cr := rec.Header().Get("Content-Range"); cr != fmt.Sprintf("bytes %d-%d/%d", wantStart, wantStart+wantLen-1, len(data)) {
		t.Fatalf("Content-Range = %q, want the requested window with the real total", cr)
	}
}

// A well-behaved upstream must still be cached, or the spool stops absorbing the
// seek storms it exists to absorb.
func TestSpoolCachesWhenUpstreamHonoursTheWindow(t *testing.T) {
	const (
		path      = "/debrid/torbox/456/file/0/movie.mkv"
		wantStart = int64(3_000_000)
		wantLen   = int64(256)
	)
	data := spoolTestPayload(8 << 20)
	provider := &rangeIgnoringProvider{data: data}
	pool := newStreamPool(nil)
	defer pool.close()
	handler := &VideoHandler{streamer: provider, streamPool: pool}

	request := func(start, length int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/video/stream", nil)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+length-1))
		rec := httptest.NewRecorder()
		if _, err := handler.streamViaProvider(rec, req, path); err != nil {
			t.Fatalf("streamViaProvider: %v", err)
		}
		return rec
	}

	request(wantStart, wantLen)
	callsAfterFirst := provider.calls

	// A nearby read inside the same window must be served from memory.
	rec := request(wantStart+512, wantLen)
	if provider.calls != callsAfterFirst {
		t.Fatalf("provider calls = %d, want %d (nearby read was not served from cache)", provider.calls, callsAfterFirst)
	}
	got := rec.Body.Bytes()
	want := data[wantStart+512 : wantStart+512+wantLen]
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cached byte %d = %d, want %d", i, got[i], want[i])
		}
	}
}
