package credits

import (
	"testing"
	"time"
)

func TestNewDetector(t *testing.T) {
	d := NewDetector()
	if d == nil {
		t.Fatal("expected non-nil detector")
	}
	if d.cache == nil {
		t.Fatal("expected non-nil cache")
	}
	if cap(d.sem) != 2 {
		t.Fatalf("expected semaphore capacity 2, got %d", cap(d.sem))
	}
}

func TestGet_ReturnsNilWhenNotCached(t *testing.T) {
	d := NewDetector()
	result := d.Get("nonexistent/path")
	if result != nil {
		t.Fatal("expected nil for uncached path")
	}
}

func TestGet_ReturnsCachedResult(t *testing.T) {
	d := NewDetector()
	expected := &DetectionResult{Detected: true, CreditsStartSec: 3562.0}
	d.mu.Lock()
	d.cache["test/path"] = expected
	d.mu.Unlock()

	result := d.Get("test/path")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !result.Detected {
		t.Fatal("expected detected=true")
	}
	if result.CreditsStartSec != 3562.0 {
		t.Fatalf("expected credits_start_sec=3562.0, got %f", result.CreditsStartSec)
	}
}

func TestIsInflight(t *testing.T) {
	d := NewDetector()

	if d.IsInflight("test/path") {
		t.Fatal("expected not inflight initially")
	}

	d.inflight.Store("test/path", struct{}{})
	if !d.IsInflight("test/path") {
		t.Fatal("expected inflight after store")
	}
}

func TestDetectAsync_DeduplicatesRequests(t *testing.T) {
	d := NewDetector()

	// Pre-cache a result
	d.mu.Lock()
	d.cache["cached/path"] = &DetectionResult{Detected: false}
	d.mu.Unlock()

	// DetectAsync should be a no-op for already-cached paths
	d.DetectAsync(nil, "cached/path", "http://example.com/video.mkv", 3600)

	// Give goroutine a moment to start (it shouldn't)
	time.Sleep(50 * time.Millisecond)

	if d.IsInflight("cached/path") {
		t.Fatal("should not start detection for already-cached path")
	}
}
