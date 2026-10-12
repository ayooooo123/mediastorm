package importer

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/javi11/nntppool/v2"
	"github.com/javi11/nntppool/v2/pkg/nntpcli"
	"github.com/javi11/nzbparser"
)

// testHeaderReader returns fixed yEnc headers instead of parsing an article body.
type testHeaderReader struct {
	fileName string
	fileSize int64
	partSize int64
}

func (r *testHeaderReader) Read([]byte) (int, error) { return 0, io.EOF }
func (r *testHeaderReader) Close() error             { return nil }
func (r *testHeaderReader) GetYencHeaders() (nntpcli.YencHeaders, error) {
	return nntpcli.YencHeaders{FileName: r.fileName, FileSize: r.fileSize, PartSize: r.partSize}, nil
}

// trackPool is a UsenetConnectionPool that serves fixed yEnc headers (the last
// segment id gets lastPart, every other segment gets basePart) while recording
// the maximum number of concurrently in-flight BodyReader calls. An optional
// per-read block simulates network latency so overlap on the hot path is
// observable.
type trackPool struct {
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	block       time.Duration // per-fetch delay applied to every BodyReader
	lastBlock   time.Duration // extra delay applied only to lastID (fail-fast test)
	fileName    string
	fileSize    int64
	basePart    int64 // uniform body part size
	lastPart    int64 // distinct final part size
	lastID      string
	// unavailableID, when set, makes BodyReader return an all-provider-miss
	// article error for that message id (fail-fast path in the parser).
	unavailableID string
}

func (p *trackPool) BodyReader(_ context.Context, msgID string, _ []string) (nntpcli.ArticleBodyReader, error) {
	p.mu.Lock()
	p.inFlight++
	if p.inFlight > p.maxInFlight {
		p.maxInFlight = p.inFlight
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.inFlight--
		p.mu.Unlock()
	}()

	if msgID == p.unavailableID {
		return nil, fmt.Errorf("article not found in any of the providers: %w", ErrArticleUnavailable)
	}
	if msgID == p.lastID && p.lastBlock > 0 {
		time.Sleep(p.lastBlock)
	} else if p.block > 0 {
		time.Sleep(p.block)
	}
	part := p.basePart
	if msgID == p.lastID {
		part = p.lastPart
	}
	return &testHeaderReader{fileName: p.fileName, fileSize: p.fileSize, partSize: part}, nil
}

func (p *trackPool) GetConnection(context.Context, []string, bool) (nntppool.PooledConnection, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *trackPool) Body(context.Context, string, io.Writer, []string) (int64, error) {
	return 0, fmt.Errorf("not implemented")
}
func (p *trackPool) Post(context.Context, io.Reader) error { return fmt.Errorf("not implemented") }
func (p *trackPool) Stat(context.Context, string, []string) (int, error) {
	return 0, fmt.Errorf("not implemented")
}
func (p *trackPool) GetProvidersInfo() []nntppool.ProviderInfo { return nil }
func (p *trackPool) GetProviderStatus(string) (*nntppool.ProviderInfo, bool) {
	return nil, false
}
func (p *trackPool) BodyBatch(context.Context, string, []nntppool.BodyBatchRequest) []nntppool.BodyBatchResult {
	return nil
}
func (p *trackPool) TestProviderPipelineSupport(context.Context, string, string) (bool, int, error) {
	return false, 0, nil
}
func (p *trackPool) GetMetrics() *nntppool.PoolMetrics { return nil }
func (p *trackPool) GetMetricsSnapshot() nntppool.PoolMetricsSnapshot {
	return nntppool.PoolMetricsSnapshot{}
}
func (p *trackPool) Quit() {}

// trackManager implements pool.Manager with a fixed "free connections" figure so
// tests can drive the fair-share concurrency heuristic deterministically.
type trackManager struct {
	pool      *trackPool
	available int
}

func (m *trackManager) GetPool() (nntppool.UsenetConnectionPool, error) { return m.pool, nil }
func (m *trackManager) SetProviders([]nntppool.UsenetProviderConfig) error {
	return nil
}
func (m *trackManager) ClearPool() error          { return nil }
func (m *trackManager) HasPool() bool             { return m.pool != nil }
func (m *trackManager) AvailableConnections() int { return m.available }

func TestFileParserLimitComputesFairShareBoundedByCap(t *testing.T) {
	cases := []struct {
		name      string
		cap       int
		divisor   int
		available int
		want      int
	}{
		// The share budgets 1/N of the free connections for IN-FLIGHT header
		// fetches; each file parser can run first+last fetches in parallel, so
		// the budget is divided by parallelYEncFetchesPerParser.
		{"share 1/4 of free connections", 16, 4, 20, 3}, // 5 fetches -> 3 parsers
		{"share floored to minimum on tiny pool", 16, 4, 2, 2},
		{"share floored to minimum when pool saturated", 16, 4, 0, 2},
		{"share binds below the hard cap", 6, 4, 40, 5},                // 10 fetches -> 5 parsers
		{"hard cap clamps a larger share", 6, 2, 40, 6},                // 20 fetches -> 10 parsers, capped
		{"large pool share allows parallel fetch gain", 16, 4, 80, 10}, // 20 fetches -> 10
		{"cap below floor is raised to the floor", 1, 2, 40, 2},
		{"sharing disabled uses hard cap (divisor 1)", 16, 1, 1000, 16},
		{"sharing disabled uses hard cap (divisor 0)", 16, 0, 1000, 16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewParser(&trackManager{pool: &trackPool{}, available: tc.available})
			p.SetConcurrency(tc.cap, tc.divisor)
			if got := p.fileParserLimit(); got != tc.want {
				t.Fatalf("fileParserLimit() = %d, want %d", got, tc.want)
			}
		})
	}
}

// multiSegmentFile builds an NzbFile with one uniform body part size per segment
// (except the last), the shape a real multi-part RAR volume takes.
func multiSegmentFile(n int, declared int, lastID string) nzbparser.NzbFile {
	segs := make([]nzbparser.NzbSegment, n)
	for i := 0; i < n; i++ {
		segs[i] = nzbparser.NzbSegment{ID: fmt.Sprintf("seg%d", i), Number: i + 1, Bytes: declared}
	}
	return nzbparser.NzbFile{
		Filename: "Movie.2024.1080p.mkv",
		Subject:  "Movie.2024.1080p.mkv yEnc (1/" + fmt.Sprint(n) + ")",
		Segments: segs,
	}
}

func TestParseFileFetchesFirstAndLastHeadersConcurrently(t *testing.T) {
	pool := &trackPool{
		block:    50 * time.Millisecond, // per header fetch
		fileName: "Movie.2024.1080p.mkv",
		fileSize: 1_000_000,
		basePart: 750_000,
		lastPart: 400_000,
		lastID:   "seg3",
	}
	p := NewParser(&trackManager{pool: pool, available: 40})

	file := multiSegmentFile(4, 800_000, "seg3")

	start := time.Now()
	parsed, err := p.parseFileWithContext(context.Background(), file, nil, nil, "x.nzb")
	if err != nil {
		t.Fatalf("parseFileWithContext: %v", err)
	}
	elapsed := time.Since(start)

	// The first and last header fetches are independent and must overlap: at
	// least two BodyReaders in flight at once.
	if pool.maxInFlight < 2 {
		t.Fatalf("max concurrent header fetches = %d, want >= 2 (first+last in parallel)", pool.maxInFlight)
	}
	// Serial would be ~2x50ms; parallel is ~1x50ms (+ overhead).
	if elapsed >= 90*time.Millisecond {
		t.Fatalf("first+last fetched serially: elapsed %v for a 2x50ms block", elapsed)
	}

	// Sizing/metadata must be unchanged by the parallel fetch: uniform body size
	// from the first segment, distinct final size, yEnc filename/file size.
	wantBytes := []int{750_000, 750_000, 750_000, 400_000}
	for i, seg := range parsed.Segments {
		if int(seg.SegmentSize) != wantBytes[i] {
			t.Errorf("segment %d size = %d, want %d", i, seg.SegmentSize, wantBytes[i])
		}
	}
	if parsed.Size != 1_000_000 {
		t.Errorf("file size = %d, want yEnc header 1000000", parsed.Size)
	}
	if parsed.Filename != "Movie.2024.1080p.mkv" {
		t.Errorf("filename = %q, want yEnc header name", parsed.Filename)
	}
}

func TestParseFileTerminalFirstSegmentFailsFastWithoutWaitingOnLast(t *testing.T) {
	pool := &trackPool{
		// The first segment reports an all-provider miss immediately, while the
		// last segment would block for 800ms. The terminal verdict must fail fast.
		unavailableID: "seg0",
		lastID:        "seg3",
		lastBlock:     800 * time.Millisecond,
		fileName:      "Movie.2024.1080p.mkv",
		fileSize:      1_000_000,
		basePart:      750_000,
		lastPart:      400_000,
	}
	p := NewParser(&trackManager{pool: pool, available: 40})

	file := multiSegmentFile(4, 800_000, "seg3")

	start := time.Now()
	_, err := p.parseFileWithContext(context.Background(), file, nil, nil, "x.nzb")
	elapsed := time.Since(start)

	if !IsArticleUnavailable(err) || !IsNonRetryable(err) {
		t.Fatalf("expected non-retryable article-unavailable error, got %v", err)
	}
	// We must not wait for the 800ms last-segment probe.
	if elapsed >= 400*time.Millisecond {
		t.Fatalf("terminal first-segment error waited on its sibling probe: elapsed %v", elapsed)
	}
}

func TestParseFileConcurrencyScalesAcrossFiles(t *testing.T) {
	pool := &trackPool{
		block:    10 * time.Millisecond,
		fileName: "Movie.2024.1080p.mkv",
		fileSize: 1_000_000,
		basePart: 750_000,
		lastPart: 700_000,
		lastID:   "last",
	}
	p := NewParser(&trackManager{pool: pool, available: 32})
	p.SetConcurrency(16, 4)
	// 32 free connections / 4 = an 8-fetch budget; with first+last fetched in
	// parallel per file that allows 4 concurrent file parsers.
	if got := p.fileParserLimit(); got != 4 {
		t.Fatalf("expected parser limit 4 (32 free / 4 divisor -> 4 in-flight pairs), got %d", got)
	}

	// Build a 20-file NZB; each file carries two segments so per-file parallel
	// first/last fetches make the effect of a wider file-parser bound visible.
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="iso-8859-1"?>
<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">`)
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&sb, `<file poster="uploader@example.com" subject="[%d/20] Movie.2024.1080p - &quot;Movie.2024.1080p.r%02d&quot; yEnc (1/2)">`, i+1, i)
		sb.WriteString(`<groups><group>alt.binaries.test</group></groups>`)
		fmt.Fprintf(&sb, `<segments><segment bytes="800000" number="1">m%d-a@example.com</segment><segment bytes="800000" number="2">m%d-last@example.com</segment></segments>`, i, i)
		sb.WriteString(`</file>`)
	}
	sb.WriteString(`</nzb>`)

	parsed, err := p.ParseFileWithContext(context.Background(), strings.NewReader(sb.String()), "x.nzb")
	if err != nil {
		t.Fatalf("ParseFileWithContext: %v", err)
	}
	if len(parsed.Files) != 20 {
		t.Fatalf("parsed %d files, want 20", len(parsed.Files))
	}
	// 4 concurrent file parsers x 2 parallel header fetches each must drive far
	// more than a single fetch at a time.
	if pool.maxInFlight < 6 {
		t.Fatalf("max concurrent header fetches = %d, want >= 6 (parallel first/last across parsers)", pool.maxInFlight)
	}
}
