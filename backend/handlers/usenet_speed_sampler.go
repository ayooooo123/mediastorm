package handlers

import (
	"sync"
	"time"
)

// usenetSpeedMinInterval keeps rapid dashboard polls from producing noisy
// rates over a tiny window; polls inside it reuse the last computed rate.
const usenetSpeedMinInterval = 2 * time.Second

// usenetSpeedSampler derives a recent download rate from the pool's cumulative
// byte counter, which is all nntppool v2 exposes. The zero value is ready.
type usenetSpeedSampler struct {
	mu        sync.Mutex
	lastAt    time.Time
	lastBytes int64
	rate      float64
}

// sample records a cumulative byte count observed at now and returns the
// bytes/sec rate since the previous sample.
func (s *usenetSpeedSampler) sample(now time.Time, totalBytes int64) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	// First sample, or the counter went backwards (pool rebuilt): rebase.
	if s.lastAt.IsZero() || totalBytes < s.lastBytes {
		s.lastAt = now
		s.lastBytes = totalBytes
		s.rate = 0
		return 0
	}

	elapsed := now.Sub(s.lastAt)
	if elapsed < usenetSpeedMinInterval {
		return s.rate
	}

	s.rate = float64(totalBytes-s.lastBytes) / elapsed.Seconds()
	s.lastAt = now
	s.lastBytes = totalBytes
	return s.rate
}
