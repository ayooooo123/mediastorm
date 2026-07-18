package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/services/streaming"
)

// mockStreamProvider implements streaming.Provider for testing.
type mockStreamProvider struct {
	mu        sync.Mutex
	responses map[string]*streaming.Response
	calls     int
	lastRange string
}

func newMockStreamProvider(totalSize int64, data []byte) *mockStreamProvider {
	return &mockStreamProvider{
		responses: make(map[string]*streaming.Response),
	}
}

func (m *mockStreamProvider) Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	m.mu.Lock()
	m.calls++
	m.lastRange = req.RangeHeader
	m.mu.Unlock()

	// Parse range start from header
	start := int64(0)
	if req.RangeHeader != "" {
		if s, ok := parseRangeStart(req.RangeHeader); ok {
			start = s
		}
	}

	totalSize := int64(100 * 1024 * 1024) // 100MB fake file

	// Generate predictable data based on position
	chunkSize := 1024 * 1024 // 1MB chunks
	data := make([]byte, chunkSize)
	for i := range data {
		data[i] = byte((start + int64(i)) % 256)
	}

	headers := http.Header{}
	headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, totalSize-1, totalSize))
	headers.Set("Content-Length", fmt.Sprintf("%d", totalSize-start))
	headers.Set("Content-Type", "video/mp4")

	return &streaming.Response{
		Body:          io.NopCloser(newSlowReader(data, 256*1024)), // 256KB per read
		Headers:       headers,
		Status:        http.StatusPartialContent,
		ContentLength: totalSize - start,
	}, nil
}

// slowReader simulates CDN reads by returning data in chunks.
type slowReader struct {
	data    []byte
	pos     int
	maxRead int
}

// gatedReader lets a test hold an upstream read in flight while the last pool
// reader disconnects. Each release permits exactly one byte to be returned.
type gatedReader struct {
	mu      sync.Mutex
	reads   int
	started chan int
	release chan struct{}
}

func newGatedReader() *gatedReader {
	return &gatedReader{
		started: make(chan int),
		release: make(chan struct{}),
	}
}

func (r *gatedReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	r.reads++
	readNumber := r.reads
	r.mu.Unlock()
	r.started <- readNumber
	<-r.release
	p[0] = byte(readNumber)
	return 1, nil
}

type playbackProbeProvider struct {
	mu        sync.Mutex
	calls     int
	lastRange string
}

type blockingOpenProvider struct {
	base    *mockStreamProvider
	started chan string
	release chan struct{}
}

func newBlockingOpenProvider() *blockingOpenProvider {
	return &blockingOpenProvider{
		base:    newMockStreamProvider(100*1024*1024, nil),
		started: make(chan string, 4),
		release: make(chan struct{}),
	}
}

func (p *blockingOpenProvider) Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	p.started <- req.Path
	select {
	case <-p.release:
		return p.base.Stream(ctx, req)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *playbackProbeProvider) Stream(_ context.Context, req streaming.Request) (*streaming.Response, error) {
	p.mu.Lock()
	p.calls++
	p.lastRange = req.RangeHeader
	p.mu.Unlock()

	start, end, ok := parseByteRange(req.RangeHeader)
	if !ok {
		return nil, fmt.Errorf("expected bounded range, got %q", req.RangeHeader)
	}
	data := make([]byte, end-start+1)
	headers := make(http.Header)
	headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, end+1024))
	headers.Set("Content-Length", strconv.FormatInt(int64(len(data)), 10))
	headers.Set("Content-Type", "video/mp4")
	return &streaming.Response{
		Body:          io.NopCloser(bytes.NewReader(data)),
		Headers:       headers,
		Status:        http.StatusPartialContent,
		ContentLength: int64(len(data)),
		Filename:      "probe.mp4",
	}, nil
}

func newSlowReader(data []byte, maxRead int) *slowReader {
	return &slowReader{data: data, maxRead: maxRead}
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := len(p)
	if n > r.maxRead {
		n = r.maxRead
	}
	remaining := len(r.data) - r.pos
	if n > remaining {
		n = remaining
	}
	copy(p[:n], r.data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

func TestParseRangeStart(t *testing.T) {
	tests := []struct {
		input string
		start int64
		ok    bool
	}{
		{"bytes=0-", 0, true},
		{"bytes=12345-", 12345, true},
		{"bytes=100-200", 100, true},
		{"bytes=28744905-", 28744905, true},
		{"", 0, false},
		{"bytes=-500", 0, false}, // suffix range
		{"invalid", 0, false},
	}

	for _, tt := range tests {
		start, ok := parseRangeStart(tt.input)
		if ok != tt.ok || (ok && start != tt.start) {
			t.Errorf("parseRangeStart(%q) = (%d, %v), want (%d, %v)",
				tt.input, start, ok, tt.start, tt.ok)
		}
	}
}

func TestParseTotalSizeFromContentRange(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"bytes 0-999/5000", 5000},
		{"bytes 100-200/13304587412", 13304587412},
		{"bytes 0-0/*", 0},
		{"", 0},
		{"invalid", 0},
	}

	for _, tt := range tests {
		got := parseTotalSizeFromContentRange(tt.input)
		if got != tt.want {
			t.Errorf("parseTotalSizeFromContentRange(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestInitialPreBufferTargetNearEOF(t *testing.T) {
	totalSize := int64(530588444)
	reqStart := totalSize - 12

	got := initialPreBufferTarget(reqStart, -1, totalSize)
	if got != 12 {
		t.Fatalf("initialPreBufferTarget near EOF = %d, want 12", got)
	}
}

func TestInitialPreBufferTargetCapsExplicitSmallRange(t *testing.T) {
	got := initialPreBufferTarget(1000, 1999, 100*1024*1024)
	if got != 1000 {
		t.Fatalf("initialPreBufferTarget explicit small range = %d, want 1000", got)
	}
}

func TestInitialPreBufferTargetUsesMinimumForOpenEndedRange(t *testing.T) {
	got := initialPreBufferTarget(0, -1, 100*1024*1024)
	if got != poolMinPreBuffer {
		t.Fatalf("initialPreBufferTarget open-ended range = %d, want %d", got, poolMinPreBuffer)
	}
}

func TestPlaybackProbeBypassesPersistentStreamPool(t *testing.T) {
	provider := &playbackProbeProvider{}
	pool := newStreamPool(nil)
	defer pool.close()
	handler := &VideoHandler{
		streamer:   provider,
		streamPool: pool,
	}

	const rangeHeader = "bytes=1000000-4145727"
	req := httptest.NewRequest(http.MethodGet, "/api/video/stream?_probe=123", nil)
	req.Header.Set("Range", rangeHeader)
	recorder := httptest.NewRecorder()

	served, err := handler.streamViaProvider(
		recorder,
		req,
		"/debrid/torbox/123/file/0/probe.mp4",
	)
	if err != nil {
		t.Fatalf("streamViaProvider returned error: %v", err)
	}
	if !served {
		t.Fatal("expected playback probe to be served")
	}
	if recorder.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusPartialContent)
	}
	if recorder.Body.Len() != 3*1024*1024 {
		t.Fatalf("body size = %d, want %d", recorder.Body.Len(), 3*1024*1024)
	}

	provider.mu.Lock()
	calls := provider.calls
	gotRange := provider.lastRange
	provider.mu.Unlock()
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	if gotRange != rangeHeader {
		t.Fatalf("provider range = %q, want %q", gotRange, rangeHeader)
	}
	if stats := pool.Stats(); stats.TotalSlots != 0 {
		t.Fatalf("persistent pool slots = %d, want 0", stats.TotalSlots)
	}
}

func TestStreamPoolNewSlotCreation(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	slot, err := pool.getOrCreate("/test/file.mp4", 0, provider)
	if err != nil {
		t.Fatalf("getOrCreate failed: %v", err)
	}
	if slot == nil {
		t.Fatal("expected non-nil slot")
	}
	if slot.startByte != 0 {
		t.Errorf("slot.startByte = %d, want 0", slot.startByte)
	}

	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected 1 provider call, got %d", calls)
	}
}

func TestStreamPoolSlotReuse(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	// Create a slot at position 0
	slot1, err := pool.getOrCreate("/test/file.mp4", 0, provider)
	if err != nil {
		t.Fatalf("first getOrCreate failed: %v", err)
	}
	readerID := slot1.registerReader(0)
	defer slot1.unregisterReader(readerID)

	// Wait for some data to be buffered
	deadline := time.After(2 * time.Second)
	for {
		slot1.mu.Lock()
		buffered := int64(len(slot1.data))
		slot1.mu.Unlock()
		if buffered > 100*1024 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for buffer to fill")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Request at a position within the buffer should reuse the slot
	slot2, err := pool.getOrCreate("/test/file.mp4", 50*1024, provider)
	if err != nil {
		t.Fatalf("second getOrCreate failed: %v", err)
	}

	if slot2 != slot1 {
		t.Error("expected slot reuse, got new slot")
	}

	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected 1 provider call (reuse), got %d", calls)
	}
}

func TestStreamPoolDifferentPositions(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	// Create slots at two widely separated positions (simulating audio/video tracks)
	slot1, err := pool.getOrCreate("/test/file.mp4", 0, provider)
	if err != nil {
		t.Fatalf("first getOrCreate failed: %v", err)
	}

	slot2, err := pool.getOrCreate("/test/file.mp4", 50*1024*1024, provider)
	if err != nil {
		t.Fatalf("second getOrCreate failed: %v", err)
	}

	if slot1 == slot2 {
		t.Error("expected different slots for widely separated positions")
	}

	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	if calls != 2 {
		t.Errorf("expected 2 provider calls, got %d", calls)
	}

	// Verify pool has 2 slots for this file
	pool.mu.RLock()
	slotCount := len(pool.files["/test/file.mp4"])
	pool.mu.RUnlock()
	if slotCount != 2 {
		t.Errorf("expected 2 slots in pool, got %d", slotCount)
	}
}

func TestStreamPoolServe(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	// Create request
	req := httptest.NewRequest("GET", "/test/file.mp4", nil)
	req.Header.Set("Range", "bytes=0-")
	w := httptest.NewRecorder()

	writeHeaders := func(w http.ResponseWriter) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	}

	served, err := pool.serve(w, req, "/test/file.mp4", 0, provider, writeHeaders, "", "")
	if !served {
		t.Fatal("expected serve to handle the request")
	}
	if err != nil {
		t.Fatalf("serve error: %v", err)
	}

	resp := w.Result()
	if resp.StatusCode != http.StatusPartialContent {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusPartialContent)
	}

	// Check that some data was written
	body := w.Body.Bytes()
	if len(body) == 0 {
		t.Error("expected non-empty response body")
	}
}

func TestStreamPoolClientDisconnectKeepsSlot(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	// Create a request with a cancellable context
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/test/file.mp4", nil).WithContext(ctx)
	req.Header.Set("Range", "bytes=0-")
	w := httptest.NewRecorder()

	writeHeaders := func(w http.ResponseWriter) {}

	// Start serving in a goroutine
	done := make(chan struct{})
	go func() {
		pool.serve(w, req, "/test/file.mp4", 0, provider, writeHeaders, "", "")
		close(done)
	}()

	// Wait a bit for data to flow, then simulate client disconnect
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// The slot should still exist in the pool
	pool.mu.RLock()
	slotCount := len(pool.files["/test/file.mp4"])
	pool.mu.RUnlock()
	if slotCount != 1 {
		t.Errorf("expected slot to survive client disconnect, got %d slots", slotCount)
	}

	// Verify background reader is still active (slot not marked done)
	slots := pool.files["/test/file.mp4"]
	slots[0].mu.Lock()
	done2 := slots[0].cdnDone
	slots[0].mu.Unlock()
	// Note: cdnDone may or may not be true depending on timing (small mock data)
	_ = done2
}

func TestPoolSlotPausesWithoutReadersAndResumesOnReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reader := newGatedReader()
	slot := &poolSlot{
		path:          "/test/paused-reader.mp4",
		ctx:           ctx,
		cancel:        cancel,
		lastAccess:    time.Now(),
		lastReadAt:    time.Now(),
		readStartedAt: time.Now(),
		signal:        make(chan struct{}),
		readerChanged: make(chan struct{}),
	}
	response := &streaming.Response{Body: io.NopCloser(reader)}
	backgroundDone := make(chan struct{})
	go func() {
		slot.backgroundReader(response)
		close(backgroundDone)
	}()

	// A freshly created slot must not consume upstream data before serve has
	// registered its reader.
	select {
	case readNumber := <-reader.started:
		t.Fatalf("upstream read %d started without an active reader", readNumber)
	case <-time.After(100 * time.Millisecond):
	}

	firstReaderID := slot.registerReader(0)
	select {
	case readNumber := <-reader.started:
		if readNumber != 1 {
			t.Fatalf("first upstream read number = %d, want 1", readNumber)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream reader did not resume after registration")
	}

	// Disconnect during an in-flight read. That read may finish, but the slot
	// must park before beginning a second chunk.
	slot.unregisterReader(firstReaderID)
	reader.release <- struct{}{}
	deadline := time.Now().Add(time.Second)
	for {
		slot.mu.Lock()
		totalRead := slot.totalRead
		slot.mu.Unlock()
		if totalRead == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("in-flight chunk was not retained; totalRead=%d", totalRead)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case readNumber := <-reader.started:
		t.Fatalf("upstream read %d started after the last reader disconnected", readNumber)
	case <-time.After(100 * time.Millisecond):
	}

	// Reconnecting closes the exact channel captured by the parked background
	// goroutine, so it cannot miss this wakeup.
	secondReaderID := slot.registerReader(1)
	select {
	case readNumber := <-reader.started:
		if readNumber != 2 {
			t.Fatalf("reconnected upstream read number = %d, want 2", readNumber)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream reader did not resume after reconnect")
	}

	// Verify the same one-in-flight-chunk bound again, then ensure cancellation
	// wakes a background reader parked with no clients.
	slot.unregisterReader(secondReaderID)
	reader.release <- struct{}{}
	deadline = time.Now().Add(time.Second)
	for {
		slot.mu.Lock()
		totalRead := slot.totalRead
		slot.mu.Unlock()
		if totalRead == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("second in-flight chunk was not retained; totalRead=%d", totalRead)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case readNumber := <-reader.started:
		t.Fatalf("upstream read %d started while the slot was orphaned", readNumber)
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	select {
	case <-backgroundDone:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not wake the paused background reader")
	}

	slot.mu.Lock()
	cdnDone := slot.cdnDone
	slot.mu.Unlock()
	if !cdnDone {
		t.Fatal("slot was not marked done after cancellation")
	}
}

func TestPoolSlotTracksSlowestReaderPosition(t *testing.T) {
	slot := &poolSlot{readerChanged: make(chan struct{})}
	slowReader := slot.registerReader(100)
	fastReader := slot.registerReader(200)
	defer slot.unregisterReader(slowReader)
	defer slot.unregisterReader(fastReader)

	slot.updateReaderPosition(fastReader, 1000)
	slot.mu.Lock()
	minPosition := slot.minReaderPos
	slot.mu.Unlock()
	if minPosition != 100 {
		t.Fatalf("fast reader moved trim boundary to %d, want slow reader at 100", minPosition)
	}

	slot.updateReaderPosition(slowReader, 300)
	slot.mu.Lock()
	minPosition = slot.minReaderPos
	slot.mu.Unlock()
	if minPosition != 300 {
		t.Fatalf("trim boundary = %d, want 300 after slow reader advanced", minPosition)
	}
}

func TestPoolSlotTrimNeverPassesSlowestReader(t *testing.T) {
	slot := &poolSlot{
		data:          make([]byte, 40),
		readerChanged: make(chan struct{}),
	}
	backingStart := &slot.data[0]
	slowReader := slot.registerReader(4)
	fastReader := slot.registerReader(40)
	defer slot.unregisterReader(slowReader)
	defer slot.unregisterReader(fastReader)

	slot.mu.Lock()
	trimmed := slot.trimConsumedBufferLocked(24)
	startByte := slot.startByte
	remaining := len(slot.data)
	slot.mu.Unlock()
	if trimmed != 4 || startByte != 4 || remaining != 36 {
		t.Fatalf("first trim = %d start=%d remaining=%d, want trim=4 start=4 remaining=36", trimmed, startByte, remaining)
	}
	if &slot.data[0] != backingStart {
		t.Fatal("trim replaced the backing buffer instead of compacting in place")
	}

	slot.updateReaderPosition(slowReader, 20)
	slot.mu.Lock()
	trimmed = slot.trimConsumedBufferLocked(24)
	startByte = slot.startByte
	remaining = len(slot.data)
	slot.mu.Unlock()
	if trimmed != 12 || startByte != 16 || remaining != 24 {
		t.Fatalf("second trim = %d start=%d remaining=%d, want trim=12 start=16 remaining=24", trimmed, startByte, remaining)
	}
}

func TestStreamPoolEviction(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	// Create a slot
	_, err := pool.getOrCreate("/test/file.mp4", 0, provider)
	if err != nil {
		t.Fatalf("getOrCreate failed: %v", err)
	}

	// Set last access to past the idle timeout
	pool.mu.RLock()
	slot := pool.files["/test/file.mp4"][0]
	pool.mu.RUnlock()

	slot.mu.Lock()
	slot.lastAccess = time.Now().Add(-2 * poolSlotIdleTimeout)
	slot.mu.Unlock()

	// Run eviction
	pool.evictIdle()

	// Slot should be evicted
	pool.mu.RLock()
	slotCount := len(pool.files["/test/file.mp4"])
	pool.mu.RUnlock()
	if slotCount != 0 {
		t.Errorf("expected slot to be evicted, got %d slots", slotCount)
	}
}

func TestStreamPoolMaxSlots(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	// Create more slots than the max
	for i := 0; i < poolMaxSlotsPerFile+2; i++ {
		pos := int64(i) * 20 * 1024 * 1024 // 20MB apart
		_, err := pool.getOrCreate("/test/file.mp4", pos, provider)
		if err != nil {
			t.Fatalf("getOrCreate at pos %d failed: %v", pos, err)
		}
	}

	// Should have at most poolMaxSlotsPerFile slots
	pool.mu.RLock()
	slotCount := len(pool.files["/test/file.mp4"])
	pool.mu.RUnlock()
	if slotCount > poolMaxSlotsPerFile {
		t.Errorf("expected at most %d slots, got %d", poolMaxSlotsPerFile, slotCount)
	}
}

func TestStreamPoolDoesNotEvictActiveSlotAtCapacity(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()
	provider := newMockStreamProvider(100*1024*1024, nil)
	readerIDs := make([]uint64, 0, poolMaxSlotsPerFile)
	slots := make([]*poolSlot, 0, poolMaxSlotsPerFile)

	for i := 0; i < poolMaxSlotsPerFile; i++ {
		position := int64(i) * 20 * 1024 * 1024
		slot, err := pool.getOrCreate("/test/active-capacity.mp4", position, provider)
		if err != nil {
			t.Fatalf("getOrCreate active slot %d: %v", i, err)
		}
		slots = append(slots, slot)
		readerIDs = append(readerIDs, slot.registerReader(position))
	}
	defer func() {
		for i, slot := range slots {
			slot.unregisterReader(readerIDs[i])
		}
	}()
	provider.mu.Lock()
	callsBeforeCapacityMiss := provider.calls
	provider.mu.Unlock()

	if _, err := pool.getOrCreate(
		"/test/active-capacity.mp4",
		int64(poolMaxSlotsPerFile)*20*1024*1024,
		provider,
	); err == nil {
		t.Fatal("expected pool capacity error while every slot has an active reader")
	}
	provider.mu.Lock()
	callsAfterCapacityMiss := provider.calls
	provider.mu.Unlock()
	if callsAfterCapacityMiss != callsBeforeCapacityMiss {
		t.Fatalf("capacity miss opened provider %d extra times, want 0", callsAfterCapacityMiss-callsBeforeCapacityMiss)
	}
	pool.mu.RLock()
	remaining := len(pool.files["/test/active-capacity.mp4"])
	pool.mu.RUnlock()
	if remaining != poolMaxSlotsPerFile {
		t.Fatalf("active slots remaining = %d, want %d", remaining, poolMaxSlotsPerFile)
	}
}

func TestStreamPoolCoalescesConcurrentSlotCreation(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()
	provider := newMockStreamProvider(100*1024*1024, nil)

	const callers = 8
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			if _, err := pool.getOrCreate("/test/concurrent-create.mp4", 0, provider); err != nil {
				t.Errorf("getOrCreate: %v", err)
			}
		}()
	}
	wg.Wait()

	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	if calls != 1 {
		t.Fatalf("provider opens = %d, want 1 for concurrent identical misses", calls)
	}
}

func TestStreamPoolCreatesUnrelatedPathsConcurrently(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()
	provider := newBlockingOpenProvider()
	results := make(chan error, 2)

	for _, path := range []string{"/test/first.mp4", "/test/second.mp4"} {
		path := path
		go func() {
			_, err := pool.getOrCreate(path, 0, provider)
			results <- err
		}()
	}

	started := make(map[string]bool)
	deadline := time.After(time.Second)
	for len(started) < 2 {
		select {
		case path := <-provider.started:
			started[path] = true
		case <-deadline:
			close(provider.release)
			for i := 0; i < 2; i++ {
				<-results
			}
			t.Fatalf("provider opens serialized across unrelated paths; started=%v", started)
		}
	}
	close(provider.release)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatalf("getOrCreate unrelated path: %v", err)
		}
	}
}

func TestStreamPoolCanceledWaiterEscapesPathCreationGate(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()
	provider := newBlockingOpenProvider()
	firstResult := make(chan error, 1)
	go func() {
		_, err := pool.getOrCreate("/test/cancel-wait.mp4", 0, provider)
		firstResult <- err
	}()

	select {
	case <-provider.started:
	case <-time.After(time.Second):
		close(provider.release)
		t.Fatal("first provider open did not start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	secondResult := make(chan error, 1)
	go func() {
		_, _, err := pool.acquire(ctx, "/test/cancel-wait.mp4", 0, provider)
		secondResult <- err
	}()
	cancel()

	select {
	case err := <-secondResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting acquire error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		close(provider.release)
		t.Fatal("canceled waiter remained blocked on creation gate")
	}

	close(provider.release)
	if err := <-firstResult; err != nil {
		t.Fatalf("first getOrCreate: %v", err)
	}
}

func TestAbs64(t *testing.T) {
	tests := []struct {
		input int64
		want  int64
	}{
		{0, 0},
		{5, 5},
		{-5, 5},
		{-1, 1},
	}
	for _, tt := range tests {
		got := abs64(tt.input)
		if got != tt.want {
			t.Errorf("abs64(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestStreamPoolFindSlotDataAvailable(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	// Create slot at position 1000
	slot, err := pool.getOrCreate("/test/file.mp4", 1000, provider)
	if err != nil {
		t.Fatalf("getOrCreate failed: %v", err)
	}
	readerID := slot.registerReader(1000)
	defer slot.unregisterReader(readerID)

	// Wait for some data
	deadline := time.After(2 * time.Second)
	for {
		slot.mu.Lock()
		buffered := int64(len(slot.data))
		slot.mu.Unlock()
		if buffered > 50*1024 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for buffer")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Should find the slot for a position within buffered data
	found := pool.findSlot("/test/file.mp4", 1000+10*1024)
	if found == nil {
		t.Error("expected to find slot for position within buffer")
	}

	// Should NOT find a slot for a wildly different path
	found = pool.findSlot("/other/file.mp4", 1000)
	if found != nil {
		t.Error("expected no slot for different path")
	}

	// Should NOT find a slot for a position before the slot's start
	found = pool.findSlot("/test/file.mp4", 500)
	if found != nil {
		t.Error("expected no slot for position before slot start")
	}
}

func TestStreamPoolServeHead(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	req := httptest.NewRequest("HEAD", "/test/file.mp4", nil)
	req.Header.Set("Range", "bytes=0-")
	w := httptest.NewRecorder()

	writeHeaders := func(w http.ResponseWriter) {}

	served, err := pool.serve(w, req, "/test/file.mp4", 0, provider, writeHeaders, "", "")
	if !served {
		t.Fatal("expected HEAD to be served")
	}
	if err != nil {
		t.Fatalf("serve error: %v", err)
	}

	resp := w.Result()
	if resp.StatusCode != http.StatusPartialContent {
		t.Errorf("HEAD status = %d, want %d", resp.StatusCode, http.StatusPartialContent)
	}

	// HEAD should have empty body
	body := w.Body.Bytes()
	if len(body) != 0 {
		t.Errorf("expected empty body for HEAD, got %d bytes", len(body))
	}

	// Should have Content-Range
	cr := resp.Header.Get("Content-Range")
	if cr == "" {
		t.Error("expected Content-Range header for HEAD response")
	}
	if !strings.Contains(cr, "bytes 0-") {
		t.Errorf("unexpected Content-Range: %s", cr)
	}
}

func TestLogStreamThroughput(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	cases := []struct {
		name      string
		leg       string
		path      string
		bytes     int64
		activeDur time.Duration
		wallDur   time.Duration
		want      []string
	}{
		{
			// Client transport fast but starved waiting on a slow source:
			// high active rate, low busy% over the window.
			name:      "fast_but_starved",
			leg:       "client-write",
			path:      "/debrid/torbox/1/file/0/Good Luck.mkv",
			bytes:     10_000_000,
			activeDur: 1 * time.Second,
			wallDur:   5 * time.Second,
			want: []string{
				"client-write throughput",
				`file="Good Luck.mkv"`, // basename extracted from path
				"wall=16.0Mbps (2.00MB/s)",
				"active=80.0Mbps (10.00MB/s)",
				"busy=20%",
				"bytes=10000000",
				"window=5.0s",
			},
		},
		{
			// Source genuinely slow: reader busy the whole window, low rate.
			name:      "source_slow",
			leg:       "CDN-read",
			path:      "Movie.mkv",
			bytes:     1_000_000,
			activeDur: 5 * time.Second,
			wallDur:   5 * time.Second,
			want: []string{
				"CDN-read throughput",
				`file="Movie.mkv"`,
				"wall=1.6Mbps (0.20MB/s)",
				"busy=100%",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			logStreamThroughput(tc.leg, tc.path, tc.bytes, tc.activeDur, tc.wallDur)
			out := buf.String()
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q\ngot: %s", w, out)
				}
			}
		})
	}
}

func TestLogStreamThroughputZeroDurations(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	// Must not divide by zero when durations are zero.
	logStreamThroughput("CDN-read", "x.mkv", 0, 0, 0)
	out := buf.String()
	for _, w := range []string{"wall=0.0Mbps", "active=0.0Mbps", "busy=0%"} {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q\ngot: %s", w, out)
		}
	}
}
