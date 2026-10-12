package handlers

import (
	"testing"
	"time"
)

func TestUsenetSpeedSampler(t *testing.T) {
	var s usenetSpeedSampler
	t0 := time.Unix(1_000, 0)

	if got := s.sample(t0, 1_000); got != 0 {
		t.Fatalf("first sample = %v, want 0", got)
	}
	if got := s.sample(t0.Add(4*time.Second), 9_000); got != 2_000 {
		t.Fatalf("rate = %v, want 2000", got)
	}
	// Inside the minimum interval the previous rate is reused.
	if got := s.sample(t0.Add(5*time.Second), 50_000); got != 2_000 {
		t.Fatalf("rapid poll rate = %v, want 2000", got)
	}
	// Counter reset (pool rebuilt) rebases instead of reporting a negative rate.
	if got := s.sample(t0.Add(10*time.Second), 100); got != 0 {
		t.Fatalf("after reset = %v, want 0", got)
	}
	if got := s.sample(t0.Add(12*time.Second), 4_100); got != 2_000 {
		t.Fatalf("rate after reset = %v, want 2000", got)
	}
}
