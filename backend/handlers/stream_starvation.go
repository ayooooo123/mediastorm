package handlers

import (
	"context"
	"sync/atomic"
	"time"
)

const (
	pipelineStarvationTimeout       = 8 * time.Second
	pipelineStarvationCheckInterval = time.Second
)

// pipelineBlockWatch measures one blocking pipeline operation. Separate watches
// for upstream reads and client writes let logs identify which leg is stalled;
// frontend buffer telemetry decides whether that blockage is actually harming
// playback before migration is requested.
type pipelineBlockWatch struct {
	startedAtNanos int64
}

func (w *pipelineBlockWatch) begin() {
	atomic.StoreInt64(&w.startedAtNanos, time.Now().UnixNano())
}

func (w *pipelineBlockWatch) end() {
	atomic.StoreInt64(&w.startedAtNanos, 0)
}

func (w *pipelineBlockWatch) blockedFor(now time.Time) time.Duration {
	started := atomic.LoadInt64(&w.startedAtNanos)
	if started <= 0 {
		return 0
	}
	return now.Sub(time.Unix(0, started))
}

func (w *pipelineBlockWatch) startedAt() int64 {
	return atomic.LoadInt64(&w.startedAtNanos)
}

// monitorPipelineStarvation runs until stopped and reports each blocked operation
// at most once. Returning false from onStarved leaves that operation eligible for a
// later retry, which is useful for stream-pool slots that temporarily have no
// active readers. Once the Read returns, the next blocked Read can be reported.
func monitorPipelineStarvation(
	ctx context.Context,
	watch *pipelineBlockWatch,
	timeout time.Duration,
	interval time.Duration,
	onStarved func(blockedFor time.Duration) bool,
) context.CancelFunc {
	monitorCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var reportedReadStart int64
		for {
			select {
			case <-monitorCtx.Done():
				return
			case now := <-ticker.C:
				readStart := watch.startedAt()
				if readStart == 0 {
					reportedReadStart = 0
					continue
				}
				if readStart == reportedReadStart {
					continue
				}
				blockedFor := watch.blockedFor(now)
				if blockedFor >= timeout && onStarved(blockedFor) {
					reportedReadStart = readStart
				}
			}
		}
	}()
	return cancel
}
