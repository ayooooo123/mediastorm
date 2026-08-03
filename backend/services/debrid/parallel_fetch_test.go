package debrid

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rangeServer serves deterministic bytes and records how many range requests
// overlap in time, which is what proves the read-ahead is actually parallel.
type rangeServer struct {
	data       []byte
	inFlight   atomic.Int64
	maxFlight  atomic.Int64
	requests   atomic.Int64
	holdFirst  chan struct{}
	holdOnce   sync.Once
	forbidFrom int64
}

func (s *rangeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	current := s.inFlight.Add(1)
	for {
		peak := s.maxFlight.Load()
		if current <= peak || s.maxFlight.CompareAndSwap(peak, current) {
			break
		}
	}
	defer s.inFlight.Add(-1)

	start, end, ok := parseRequestRange(r.Header.Get("Range"), int64(len(s.data)))
	if !ok {
		w.WriteHeader(http.StatusOK)
		w.Write(s.data)
		return
	}
	if s.forbidFrom > 0 && start >= s.forbidFrom {
		// Simulate a CDN that ignores the range and restarts the file.
		w.WriteHeader(http.StatusOK)
		w.Write(s.data)
		return
	}
	if s.holdFirst != nil && start == 0 {
		s.holdOnce.Do(func() { <-s.holdFirst })
	}

	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(s.data)))
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	w.WriteHeader(http.StatusPartialContent)
	w.Write(s.data[start : end+1])
}

func parseRequestRange(header string, size int64) (int64, int64, bool) {
	if !strings.HasPrefix(header, "bytes=") {
		return 0, 0, false
	}
	spec := strings.TrimPrefix(header, "bytes=")
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(spec[:dash], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	end := size - 1
	if tail := spec[dash+1:]; tail != "" {
		if parsed, parseErr := strconv.ParseInt(tail, 10, 64); parseErr == nil {
			end = parsed
		}
	}
	if end >= size {
		end = size - 1
	}
	return start, end, true
}

func payload(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251)
	}
	return data
}

func TestParallelBodyStreamsExactBytesInOrder(t *testing.T) {
	source := &rangeServer{data: payload(5 << 20)}
	server := httptest.NewServer(source)
	defer server.Close()

	body := newParallelBody(context.Background(), server.Client(), server.URL, t.Name(), 0, int64(len(source.data))-1, 4, 512<<10, 4)
	defer body.Close()

	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(source.data) {
		t.Fatalf("length = %d, want %d", len(got), len(source.data))
	}
	for i := range got {
		if got[i] != source.data[i] {
			t.Fatalf("byte %d = %d, want %d (ordering broken)", i, got[i], source.data[i])
		}
	}
	if peak := source.maxFlight.Load(); peak < 2 {
		t.Fatalf("peak concurrent range requests = %d, want >= 2 (not reading ahead)", peak)
	}
}

// The fast-start ramp only engages when chunkSize exceeds the 1 MiB first chunk,
// so every earlier test (chunkSize <= 512 KiB) clamped it away and could not see
// that the producer advanced by the ramped-up size instead of the span it had
// just queued. That dropped 1 MiB, then 2 MiB more, leaving the consumer a
// contiguous stream with holes and every later byte at the wrong offset.
func TestParallelBodyRampCoversEveryByte(t *testing.T) {
	const chunkSize = 8 << 20 // production value: ramp goes 1 -> 2 -> 4 -> 8 MiB
	source := &rangeServer{data: payload(40 << 20)}
	server := httptest.NewServer(source)
	defer server.Close()

	body := newParallelBody(
		context.Background(), server.Client(), server.URL, t.Name(),
		0, int64(len(source.data))-1, 4, chunkSize, 12,
	)
	defer body.Close()

	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(source.data) {
		t.Fatalf("length = %d, want %d (the ramp dropped %d bytes)",
			len(got), len(source.data), len(source.data)-len(got))
	}
	for i := range got {
		if got[i] != source.data[i] {
			t.Fatalf("byte %d = %d, want %d: the stream is shifted from offset %d onward",
				i, got[i], source.data[i], i)
		}
	}
}

// The same guarantee for a mid-file window, which is what a seek produces.
func TestParallelBodyRampCoversEveryByteFromAnOffset(t *testing.T) {
	const chunkSize = 8 << 20
	source := &rangeServer{data: payload(40 << 20)}
	server := httptest.NewServer(source)
	defer server.Close()

	start, end := int64(3<<20), int64(30<<20)-1
	body := newParallelBody(
		context.Background(), server.Client(), server.URL, t.Name(),
		start, end, 4, chunkSize, 12,
	)
	defer body.Close()

	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := source.data[start : end+1]
	if len(got) != len(want) {
		t.Fatalf("length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d of the window = %d, want %d (absolute offset %d)",
				i, got[i], want[i], start+int64(i))
		}
	}
}

// A DLNA renderer opens several ranges of one file at once (head, tail index,
// first video cluster). Per-request worker pools multiplied those into enough
// concurrent CDN connections to trip the provider's per-file rate limit, which
// killed the stream mid-playback. The budget is per file, so concurrent readers
// share it.
func TestParallelBodyCapsConnectionsPerFileAcrossReaders(t *testing.T) {
	const workers = 4
	source := &rangeServer{data: payload(6 << 20)}
	server := httptest.NewServer(source)
	defer server.Close()

	var wg sync.WaitGroup
	for reader := 0; reader < 3; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := newParallelBody(
				context.Background(), server.Client(), server.URL, "shared-file",
				0, int64(len(source.data))-1, workers, 512<<10, workers,
			)
			defer body.Close()
			if _, err := io.Copy(io.Discard, body); err != nil {
				t.Errorf("read: %v", err)
			}
		}()
	}
	wg.Wait()

	if peak := source.maxFlight.Load(); peak > workers {
		t.Fatalf("peak concurrent range requests = %d, want <= %d (budget not shared across readers)", peak, workers)
	}
}

// The cap must not serialise unrelated files: budgets are keyed per file, so two
// different files each get their own workers.
func TestParallelBodyBudgetsAreIndependentPerFile(t *testing.T) {
	const workers = 3
	source := &rangeServer{data: payload(6 << 20)}
	server := httptest.NewServer(source)
	defer server.Close()

	var wg sync.WaitGroup
	for reader := 0; reader < 2; reader++ {
		key := fmt.Sprintf("file-%d", reader)
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := newParallelBody(
				context.Background(), server.Client(), server.URL, key,
				0, int64(len(source.data))-1, workers, 512<<10, workers,
			)
			defer body.Close()
			if _, err := io.Copy(io.Discard, body); err != nil {
				t.Errorf("read: %v", err)
			}
		}()
	}
	wg.Wait()

	if peak := source.maxFlight.Load(); peak <= workers {
		t.Fatalf("peak concurrent range requests = %d, want > %d (separate files were serialised)", peak, workers)
	}
}

// Budgets must be discarded once their last reader is gone, or the map grows
// for every file ever streamed.
func TestFileConnBudgetsAreReleasedWhenIdle(t *testing.T) {
	const key = "release-me"
	slots := acquireFileConns(key, 2)
	acquireFileConns(key, 2)

	fileConnMu.Lock()
	pool, ok := fileConns[key]
	refs := 0
	if ok {
		refs = pool.refs
	}
	fileConnMu.Unlock()
	if !ok || refs != 2 {
		t.Fatalf("refs = %d present=%v, want 2 present", refs, ok)
	}

	releaseFileConns(key)
	releaseFileConns(key)

	fileConnMu.Lock()
	_, stillThere := fileConns[key]
	fileConnMu.Unlock()
	if stillThere {
		t.Fatal("budget retained after last reader released it")
	}

	// Re-acquiring after release must hand out a fresh budget, not a closed one.
	if reacquired := acquireFileConns(key, 2); reacquired == nil {
		t.Fatal("re-acquire returned nil")
	} else if cap(reacquired) != 2 {
		t.Fatalf("re-acquired capacity = %d, want 2", cap(reacquired))
	} else if reacquired == slots {
		t.Fatal("re-acquire reused the discarded budget")
	}
	releaseFileConns(key)
}

func TestParallelBodyServesOffsetWindow(t *testing.T) {
	source := &rangeServer{data: payload(3 << 20)}
	server := httptest.NewServer(source)
	defer server.Close()

	start, end := int64(1_000_000), int64(2_500_000)
	body := newParallelBody(context.Background(), server.Client(), server.URL, t.Name(), start, end, 3, 256<<10, 3)
	defer body.Close()

	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := source.data[start : end+1]
	if len(got) != len(want) {
		t.Fatalf("length = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("byte %d differs at offset window", i)
		}
	}
}

func TestParallelBodyPropagatesUpstreamRangeFailure(t *testing.T) {
	source := &rangeServer{data: payload(4 << 20), forbidFrom: 1 << 20}
	server := httptest.NewServer(source)
	defer server.Close()

	body := newParallelBody(context.Background(), server.Client(), server.URL, t.Name(), 0, int64(len(source.data))-1, 2, 512<<10, 2)
	defer body.Close()

	if _, err := io.ReadAll(body); err == nil {
		t.Fatal("expected an error when upstream ignores the range, got nil")
	}
}

func TestParallelBodyCloseStopsInFlightWork(t *testing.T) {
	release := make(chan struct{})
	source := &rangeServer{data: payload(8 << 20), holdFirst: release}
	server := httptest.NewServer(source)
	defer server.Close()

	body := newParallelBody(context.Background(), server.Client(), server.URL, t.Name(), 0, int64(len(source.data))-1, 4, 512<<10, 4)
	time.Sleep(150 * time.Millisecond)
	body.Close()
	close(release)

	// Closing must not deadlock and must stop issuing new requests.
	settled := source.requests.Load()
	time.Sleep(200 * time.Millisecond)
	if grew := source.requests.Load() - settled; grew > int64(4) {
		t.Fatalf("issued %d more requests after Close, want the pipeline stopped", grew)
	}
}

func TestParseContentRangeBounds(t *testing.T) {
	for _, testCase := range []struct {
		header string
		start  int64
		end    int64
		ok     bool
	}{
		{"bytes 0-1023/4096", 0, 1023, true},
		{"bytes 100-199/*", 100, 199, true},
		{"bytes */4096", 0, 0, false},
		{"items 0-10/20", 0, 0, false},
		{"bytes 500-100/4096", 0, 0, false},
		{"", 0, 0, false},
	} {
		start, end, ok := parseContentRange(testCase.header)
		if ok != testCase.ok || (ok && (start != testCase.start || end != testCase.end)) {
			t.Fatalf("parseContentRange(%q) = (%d,%d,%v), want (%d,%d,%v)",
				testCase.header, start, end, ok, testCase.start, testCase.end, testCase.ok)
		}
	}
}
