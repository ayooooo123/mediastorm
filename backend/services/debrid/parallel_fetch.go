package debrid

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Debrid CDNs throttle each connection independently: a single range request to
// TorBox sustains roughly 7-21 Mbps, which sits right at (and often below) the
// bitrate of a 4K remux, so playback starves and the renderer rebuffers. Four
// concurrent range requests against the same file aggregate to ~45 Mbps on the
// same link, so we read ahead over several connections and hand the client one
// ordered byte stream.
const (
	defaultParallelWorkers   = 4
	defaultParallelChunkSize = 8 << 20 // 8 MiB
	// Read-ahead depth is deliberately larger than the worker count: workers cap
	// how many CDN connections are open, while depth caps how far ahead we buffer.
	// 12 chunks is ~96 MiB, roughly 50 seconds of a 15 Mbps 4K stream, which rides
	// out the multi-second dips a debrid CDN produces.
	defaultParallelDepth = 12
	// Below this it is cheaper to use the connection we already opened than to
	// pay another round of CDN time-to-first-byte.
	minParallelContentLength = 32 << 20 // 32 MiB
)

func parallelDepth(workers int) int {
	if raw := strings.TrimSpace(os.Getenv("MEDIASTORM_DEBRID_PARALLEL_DEPTH")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 && parsed <= 64 {
			return parsed
		}
	}
	if defaultParallelDepth < workers {
		return workers
	}
	return defaultParallelDepth
}

func parallelWorkers() int {
	if raw := strings.TrimSpace(os.Getenv("MEDIASTORM_DEBRID_PARALLEL_WORKERS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 && parsed <= 16 {
			return parsed
		}
	}
	return defaultParallelWorkers
}

func parallelChunkSize() int64 {
	if raw := strings.TrimSpace(os.Getenv("MEDIASTORM_DEBRID_PARALLEL_CHUNK")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed >= 1<<20 && parsed <= 64<<20 {
			return parsed
		}
	}
	return defaultParallelChunkSize
}

// parseContentRange extracts the absolute inclusive bounds from a
// "bytes start-end/total" header. Any other shape reports ok=false.
func parseContentRange(value string) (start, end int64, ok bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "bytes ") {
		return 0, 0, false
	}
	spec := strings.TrimPrefix(value, "bytes ")
	if slash := strings.IndexByte(spec, '/'); slash >= 0 {
		spec = spec[:slash]
	}
	dash := strings.IndexByte(spec, '-')
	if dash <= 0 {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(spec[:dash]), 10, 64)
	if err != nil {
		return 0, 0, false
	}
	end, err = strconv.ParseInt(strings.TrimSpace(spec[dash+1:]), 10, 64)
	if err != nil || end < start {
		return 0, 0, false
	}
	return start, end, true
}

type fetchChunk struct {
	index int64
	start int64
	end   int64 // inclusive
	data  []byte
	err   error
	done  chan struct{}
}

// Concurrent CDN connections are budgeted per file across every in-flight
// client request, not per request. A single player opens one stream, so a
// per-request budget looked correct; a DLNA renderer opens several ranges at
// once (head, tail index, then the first video cluster), and each one used to
// spawn its own worker pool. Three concurrent ranges times four workers is
// twelve connections to one file, which trips the provider's per-file rate
// limit — TorBox answers 429, the stream dies mid-playback, and the renderer
// gives up after the few megabytes it managed to read.
type fileConnPool struct {
	slots chan struct{}
	refs  int
}

var (
	fileConnMu sync.Mutex
	fileConns  = make(map[string]*fileConnPool)
)

// acquireFileConns returns the connection budget shared by every reader of key.
// The first caller fixes the width; concurrent readers queue for the same slots.
func acquireFileConns(key string, workers int) chan struct{} {
	fileConnMu.Lock()
	defer fileConnMu.Unlock()
	pool, ok := fileConns[key]
	if !ok {
		pool = &fileConnPool{slots: make(chan struct{}, workers)}
		fileConns[key] = pool
	}
	pool.refs++
	return pool.slots
}

// releaseFileConns drops one reader's claim, discarding the budget once the
// last reader for a file is gone so the map cannot grow without bound.
func releaseFileConns(key string) {
	fileConnMu.Lock()
	defer fileConnMu.Unlock()
	pool, ok := fileConns[key]
	if !ok {
		return
	}
	pool.refs--
	if pool.refs <= 0 {
		delete(fileConns, key)
	}
}

// parallelBody serves [start,end] of url as one ordered stream, filled by
// several concurrent range requests running ahead of the reader.
type parallelBody struct {
	cancel  context.CancelFunc
	ordered chan *fetchChunk
	current *fetchChunk
	offset  int
	closed  bool
	once    sync.Once
}

func newParallelBody(ctx context.Context, client *http.Client, url, fileKey string, start, end int64, workers int, chunkSize int64, depth int) *parallelBody {
	ctx, cancel := context.WithCancel(ctx)
	if depth < workers {
		depth = workers
	}
	body := &parallelBody{
		cancel: cancel,
		// Depth bounds how far ahead we buffer (and therefore memory:
		// depth*chunkSize); workers bounds how many CDN connections are open.
		ordered: make(chan *fetchChunk, depth),
	}

	// Claimed before the producer starts so the budget is held for this reader's
	// whole life, and shared across every concurrent reader of the same file: a
	// renderer opening three ranges at once still cannot exceed it.
	slots := acquireFileConns(fileKey, workers)

	go func() {
		defer close(body.ordered)
		defer releaseFileConns(fileKey)
		// Every seek restarts this pipeline, and the reader cannot emit a byte
		// until chunk 0 lands. At the CDN's multi-second time-to-first-byte a
		// full-size first chunk guarantees a visible stall, so ramp 1→2→4→…→
		// chunkSize: playback resumes on a small chunk while the big ones fill in.
		size := int64(1) << 20
		if size > chunkSize {
			size = chunkSize
		}
		for index, position := int64(0), start; position <= end; index++ {
			chunkEnd := position + size - 1
			if chunkEnd > end {
				chunkEnd = end
			}
			chunk := &fetchChunk{index: index, start: position, end: chunkEnd, done: make(chan struct{})}
			// Advance by the span actually queued. Advancing by `size` after the
			// ramp widened it skips the bytes in between, and the consumer then
			// receives a contiguous stream with holes — every byte after the first
			// chunk lands at the wrong offset and the container looks corrupt.
			position = chunkEnd + 1
			if next := size * 2; next <= chunkSize {
				size = next
			} else {
				size = chunkSize
			}

			// Queue first: the producer runs ahead up to `depth`, while each
			// fetch waits its turn for one of the `workers` connection slots.
			select {
			case body.ordered <- chunk:
			case <-ctx.Done():
				return
			}

			go func(chunk *fetchChunk) {
				defer close(chunk.done)
				select {
				case slots <- struct{}{}:
					defer func() { <-slots }()
				case <-ctx.Done():
					chunk.err = ctx.Err()
					return
				}
				chunk.data, chunk.err = fetchChunkRange(ctx, client, url, chunk.start, chunk.end)
			}(chunk)
		}
	}()

	return body
}

func fetchChunkRange(ctx context.Context, client *http.Client, url string, start, end int64) ([]byte, error) {
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			select {
			case <-time.After(time.Duration(1+attempt) * time.Second):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		defer resp.Body.Close()

		if resp.StatusCode != http.StatusPartialContent {
			// Without 206 the upstream ignored the range and would restart the file,
			// which must never be spliced into the ordered stream.
			return nil, fmt.Errorf("range %d-%d: unexpected status %s", start, end, resp.Status)
		}

		want := end - start + 1
		buffer := make([]byte, want)
		if _, err := io.ReadFull(resp.Body, buffer); err != nil {
			return nil, fmt.Errorf("range %d-%d: %w", start, end, err)
		}
		return buffer, nil
	}
	return nil, fmt.Errorf("range %d-%d: gave up after 5 rate-limit responses", start, end)
}

func (b *parallelBody) Read(p []byte) (int, error) {
	for {
		if b.closed {
			return 0, io.EOF
		}
		if b.current == nil {
			chunk, ok := <-b.ordered
			if !ok {
				return 0, io.EOF
			}
			<-chunk.done
			if chunk.err != nil {
				return 0, chunk.err
			}
			b.current, b.offset = chunk, 0
		}
		if b.offset < len(b.current.data) {
			n := copy(p, b.current.data[b.offset:])
			b.offset += n
			return n, nil
		}
		b.current = nil
	}
}

func (b *parallelBody) Close() error {
	b.once.Do(func() {
		b.closed = true
		b.cancel()
		// Drain so in-flight workers observe cancellation and exit.
		go func() {
			for chunk := range b.ordered {
				<-chunk.done
			}
		}()
	})
	return nil
}

// maybeParallelBody upgrades a single-connection CDN response to a parallel
// read-ahead stream. It returns nil when the response is not a good candidate,
// in which case the caller keeps the original body.
func maybeParallelBody(ctx context.Context, client *http.Client, url, fileKey string, resp *http.Response, rarOffset int64) io.ReadCloser {
	workers := parallelWorkers()
	if workers <= 1 || rarOffset > 0 {
		return nil
	}
	if resp.Request != nil && resp.Request.Method != http.MethodGet {
		return nil
	}

	var start, end int64
	switch resp.StatusCode {
	case http.StatusPartialContent:
		parsedStart, parsedEnd, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok {
			return nil
		}
		start, end = parsedStart, parsedEnd
	case http.StatusOK:
		if resp.ContentLength <= 0 {
			return nil
		}
		start, end = 0, resp.ContentLength-1
	default:
		return nil
	}

	if end-start+1 < minParallelContentLength {
		return nil
	}

	chunkSize := parallelChunkSize()
	depth := parallelDepth(workers)
	log.Printf("[debrid-stream] parallel read-ahead: bytes=%d-%d workers=%d chunk=%dMiB depth=%d (%dMiB buffer) file=%s",
		start, end, workers, chunkSize>>20, depth, (int64(depth)*chunkSize)>>20, fileKey)

	// The pipeline refetches from the top of the window, so the connection that
	// carried the probe is no longer needed.
	resp.Body.Close()
	return newParallelBody(ctx, client, url, fileKey, start, end, workers, chunkSize, depth)
}
