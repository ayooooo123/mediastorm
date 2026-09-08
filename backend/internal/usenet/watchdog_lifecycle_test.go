package usenet

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acomagu/bufpipe"
	"github.com/javi11/nntppool"
	"go.uber.org/mock/gomock"
)

func TestWatchdogAllowsConsumedPayloadToFinish(t *testing.T) {
	for _, finalErr := range []error{nil, errors.New("invalid final checksum")} {
		name := "success"
		if finalErr != nil {
			name = "integrity-error"
		}
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			cp := nntppool.NewMockUsenetConnectionPool(ctrl)
			cp.EXPECT().GetMetricsSnapshot().Return(nntppool.PoolMetricsSnapshot{}).AnyTimes()
			pr, pw := bufpipe.New(nil)
			payload := strings.Repeat("x", 64)
			seg := &segment{Id: "complete", End: 63, SegmentSize: 64, reader: pr, writer: pw}
			defer seg.Close()
			r := &usenetReader{log: slog.New(slog.NewTextHandler(io.Discard, nil)), init: make(chan any, 1), rg: segmentRange{segments: []*segment{seg}}}
			cp.EXPECT().Body(gomock.Any(), seg.Id, gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, _ string, w io.Writer, _ []string) (int64, error) {
				n, err := w.Write([]byte(payload))
				if err != nil {
					return int64(n), err
				}
				data := make([]byte, len(payload))
				if got, err := r.Read(data); err != nil || got != len(payload) || string(data) != payload {
					t.Fatalf("Read = %d, %v", got, err)
				}
				select {
				case <-ctx.Done():
					return int64(n), ctx.Err()
				case <-time.After(80 * time.Millisecond):
					return int64(n), finalErr
				}
			})
			n, err := r.fetchSegmentBodyWithWatchdog(context.Background(), cp, seg.Id, pw, nil, seg, 20*time.Millisecond)
			if n != 64 || !errors.Is(err, finalErr) {
				t.Fatalf("fetch = %d, %v; want 64, %v", n, err, finalErr)
			}
		})
	}
}

func TestWatchdogRetiredSegmentCancelsWithoutStarvation(t *testing.T) {
	ctrl := gomock.NewController(t)
	cp := nntppool.NewMockUsenetConnectionPool(ctrl)
	pr, pw := bufpipe.New(nil)
	seg := &segment{Id: "retired", reader: pr, writer: pw}
	seg.markRequired()
	cp.EXPECT().Body(gomock.Any(), seg.Id, gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, _ string, _ io.Writer, _ []string) (int64, error) {
		if err := seg.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(time.Second):
			t.Fatal("retired fetch not cancelled")
			return 0, nil
		}
	})
	r := &usenetReader{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, err := r.fetchSegmentBodyWithWatchdog(context.Background(), cp, seg.Id, pw, nil, seg, 20*time.Millisecond)
	if !errors.Is(err, context.Canceled) || errors.Is(err, errRequiredSegmentNoProgress) {
		t.Fatalf("error = %v", err)
	}
}

func TestRequiredStallStartsWithConsumerDemand(t *testing.T) {
	seg := &segment{}
	oldProgress := time.Now().Add(-time.Hour).UnixNano()
	seg.markRequired()
	if seg.requiredStalled(time.Now(), oldProgress, time.Second) {
		t.Fatal("prefetch age counted as starvation")
	}
	if !seg.requiredStalled(time.Now().Add(2*time.Second), oldProgress, time.Second) {
		t.Fatal("active stalled read not detected")
	}
	seg.clearRequired()
	if seg.requiredStalled(time.Now().Add(2*time.Second), oldProgress, time.Second) {
		t.Fatal("inactive read counted as starvation")
	}
}

func TestConsumedSegmentFinalizationHasSeparateDeadline(t *testing.T) {
	ctrl := gomock.NewController(t)
	cp := nntppool.NewMockUsenetConnectionPool(ctrl)
	seg := &segment{Id: "finalizing"}
	cp.EXPECT().Body(gomock.Any(), seg.Id, gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, _ string, w io.Writer, _ []string) (int64, error) {
		n, err := w.Write([]byte("data"))
		if err != nil {
			return int64(n), err
		}
		atomic.StoreInt64(&seg.consumedAt, time.Now().Add(-consumedSegmentFinalizationTimeout).UnixNano())
		select {
		case <-ctx.Done():
			return int64(n), nil
		case <-time.After(time.Second):
			t.Fatal("finalization not cancelled")
			return int64(n), nil
		}
	})
	r := &usenetReader{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	n, err := r.fetchSegmentBodyWithWatchdog(context.Background(), cp, seg.Id, io.Discard, nil, seg, 20*time.Millisecond)
	if n != 4 || !errors.Is(err, errSegmentFinalizationTimeout) || errors.Is(err, errRequiredSegmentNoProgress) {
		t.Fatalf("fetch = %d, %v", n, err)
	}
}
