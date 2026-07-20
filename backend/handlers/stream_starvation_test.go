package handlers

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestMonitorPipelineStarvationOnlyFiresForBlockedOperation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var watch pipelineBlockWatch
	var calls int32
	stop := monitorPipelineStarvation(ctx, &watch, 30*time.Millisecond, 5*time.Millisecond, func(time.Duration) bool {
		atomic.AddInt32(&calls, 1)
		return true
	})
	defer stop()

	time.Sleep(45 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("monitor fired while no pipeline operation was pending: calls=%d", got)
	}

	watch.begin()
	deadline := time.After(250 * time.Millisecond)
	for atomic.LoadInt32(&calls) == 0 {
		select {
		case <-deadline:
			t.Fatal("monitor did not fire for blocked pipeline operation")
		case <-time.After(5 * time.Millisecond):
		}
	}
	watch.end()
}

func TestMonitorPipelineStarvationDisarmsWhenOperationReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var watch pipelineBlockWatch
	var calls int32
	stop := monitorPipelineStarvation(ctx, &watch, 40*time.Millisecond, 5*time.Millisecond, func(time.Duration) bool {
		atomic.AddInt32(&calls, 1)
		return true
	})
	defer stop()

	watch.begin()
	time.Sleep(10 * time.Millisecond)
	watch.end()
	time.Sleep(55 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("monitor fired after pipeline operation completed: calls=%d", got)
	}
}

func TestMonitorPipelineStarvationRearmsForNextBlockedOperation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var watch pipelineBlockWatch
	var calls int32
	stop := monitorPipelineStarvation(ctx, &watch, 25*time.Millisecond, 5*time.Millisecond, func(time.Duration) bool {
		atomic.AddInt32(&calls, 1)
		return true
	})
	defer stop()

	waitForCalls := func(want int32) {
		t.Helper()
		deadline := time.After(250 * time.Millisecond)
		for atomic.LoadInt32(&calls) < want {
			select {
			case <-deadline:
				t.Fatalf("monitor calls = %d, want %d", atomic.LoadInt32(&calls), want)
			case <-time.After(5 * time.Millisecond):
			}
		}
	}

	watch.begin()
	waitForCalls(1)
	time.Sleep(35 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("same blocked operation reported %d times, want once", got)
	}
	watch.end()
	time.Sleep(10 * time.Millisecond)

	watch.begin()
	waitForCalls(2)
	watch.end()
}
