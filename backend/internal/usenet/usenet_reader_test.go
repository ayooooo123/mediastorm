package usenet

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/acomagu/bufpipe"
	"github.com/javi11/nntppool/v2"
	"go.uber.org/mock/gomock"
)

func TestReadPreservesCancellationWithoutWalkingLookahead(t *testing.T) {
	firstReader, firstWriter := bufpipe.New(nil)
	secondReader, secondWriter := bufpipe.New(nil)
	if err := firstWriter.CloseWithError(context.Canceled); err != nil {
		t.Fatalf("first writer close: %v", err)
	}
	if err := secondWriter.Close(); err != nil {
		t.Fatalf("second writer close: %v", err)
	}

	reader := &usenetReader{
		id:   1,
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		init: make(chan any, 1),
		rg: segmentRange{segments: []*segment{
			{Id: "<required@usenet>", End: 9, SegmentSize: 10, reader: firstReader, writer: firstWriter},
			{Id: "<lookahead@usenet>", End: 9, SegmentSize: 10, reader: secondReader, writer: secondWriter},
		}},
	}

	n, err := reader.Read(make([]byte, 10))
	if n != 0 {
		t.Fatalf("Read() bytes = %d, want 0", n)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read() error = %v, want context.Canceled", err)
	}
	if reader.rg.current != 0 {
		t.Fatalf("reader advanced to segment %d after cancellation", reader.rg.current)
	}
	if second := reader.rg.segments[1]; second.isRequired() || second.BytesRead() != 0 {
		t.Fatalf("lookahead segment was consumed after cancellation: required=%v bytes=%d", second.isRequired(), second.BytesRead())
	}
}

func TestRequiredSegmentNoProgressRetriesBeforePlaybackBlocksIndefinitely(t *testing.T) {
	ctrl := gomock.NewController(t)
	cp := nntppool.NewMockUsenetConnectionPool(ctrl)
	cp.EXPECT().GetMetricsSnapshot().Return(nntppool.PoolMetricsSnapshot{}).AnyTimes()

	pipeReader, pipeWriter := bufpipe.New(nil)
	defer pipeReader.Close()
	seg := &segment{Id: "<stalled@usenet>", reader: pipeReader, writer: pipeWriter}
	seg.markRequired()

	firstAttempt := cp.EXPECT().Body(
		gomock.Any(), seg.Id, gomock.Any(), gomock.Any(),
	).DoAndReturn(func(ctx context.Context, _ string, _ io.Writer, _ []string) (int64, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	cp.EXPECT().Body(
		gomock.Any(), seg.Id, gomock.Any(), gomock.Any(),
	).After(firstAttempt).DoAndReturn(func(_ context.Context, _ string, w io.Writer, _ []string) (int64, error) {
		n, err := w.Write([]byte("recovered"))
		return int64(n), err
	})

	reader := &usenetReader{id: 2, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	started := time.Now()
	bytesFetched, err := reader.fetchSegmentBodyWithWatchdog(
		context.Background(), cp, seg.Id, pipeWriter, nil, seg, 20*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("fetch with watchdog: %v", err)
	}
	if bytesFetched != int64(len("recovered")) {
		t.Fatalf("bytes fetched = %d, want %d", bytesFetched, len("recovered"))
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("watchdog recovery took %v", elapsed)
	}
}

func TestRequiredSegmentPartialStallDoesNotDuplicateBytes(t *testing.T) {
	ctrl := gomock.NewController(t)
	cp := nntppool.NewMockUsenetConnectionPool(ctrl)
	cp.EXPECT().GetMetricsSnapshot().Return(nntppool.PoolMetricsSnapshot{}).AnyTimes()

	pipeReader, pipeWriter := bufpipe.New(nil)
	defer pipeReader.Close()
	seg := &segment{Id: "<partial-stall@usenet>", reader: pipeReader, writer: pipeWriter}
	seg.markRequired()

	cp.EXPECT().Body(
		gomock.Any(), seg.Id, gomock.Any(), gomock.Any(),
	).Times(1).DoAndReturn(func(ctx context.Context, _ string, w io.Writer, _ []string) (int64, error) {
		n, err := w.Write([]byte("partial"))
		if err != nil {
			return int64(n), err
		}
		<-ctx.Done()
		return int64(n), ctx.Err()
	})

	reader := &usenetReader{id: 3, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	bytesFetched, err := reader.fetchSegmentBodyWithWatchdog(
		context.Background(), cp, seg.Id, pipeWriter, nil, seg, 20*time.Millisecond,
	)
	if bytesFetched != int64(len("partial")) {
		t.Fatalf("bytes fetched = %d, want %d", bytesFetched, len("partial"))
	}
	if !errors.Is(err, errRequiredSegmentNoProgress) {
		t.Fatalf("error = %v, want required-segment no-progress error", err)
	}
}

func TestFetchSegmentBodyKeepsWriterSnapshotAcrossRetry(t *testing.T) {
	ctrl := gomock.NewController(t)
	cp := nntppool.NewMockUsenetConnectionPool(ctrl)
	cp.EXPECT().GetMetricsSnapshot().Return(nntppool.PoolMetricsSnapshot{}).AnyTimes()

	pipeReader, pipeWriter := bufpipe.New(nil)
	seg := &segment{
		Id:     "<retry@usenet>",
		groups: []string{"alt.test"},
		reader: pipeReader,
		writer: pipeWriter,
	}

	writerSnapshot := seg.Writer()
	if writerSnapshot == nil {
		t.Fatal("expected an open segment writer")
	}

	firstAttempt := cp.EXPECT().Body(
		gomock.Any(),
		seg.Id,
		writerSnapshot,
		seg.groups,
	).DoAndReturn(func(context.Context, string, io.Writer, []string) (int64, error) {
		if err := seg.Close(); err != nil {
			t.Fatalf("segment.Close() error = %v", err)
		}
		return 0, io.ErrClosedPipe
	})
	cp.EXPECT().Body(
		gomock.Any(),
		seg.Id,
		writerSnapshot,
		seg.groups,
	).After(firstAttempt).DoAndReturn(func(_ context.Context, _ string, w io.Writer, _ []string) (int64, error) {
		actual, ok := w.(*bufpipe.PipeWriter)
		if !ok || actual == nil {
			t.Fatalf("retry received a nil pipe writer: %#v", w)
		}
		if actual != writerSnapshot {
			t.Fatal("retry did not reuse the original writer snapshot")
		}
		return 17, nil
	})

	reader := &usenetReader{
		id:  1,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	bytesFetched, err := reader.fetchSegmentBody(
		context.Background(),
		cp,
		seg.Id,
		writerSnapshot,
		seg.groups,
	)
	if err != nil {
		t.Fatalf("fetchSegmentBody() error = %v", err)
	}
	if bytesFetched != 17 {
		t.Fatalf("fetchSegmentBody() bytes = %d, want 17", bytesFetched)
	}
	if seg.Writer() != nil {
		t.Fatal("expected segment.Close() to clear the stored writer")
	}
}
