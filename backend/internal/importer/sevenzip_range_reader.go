package importer

import (
	"container/list"
	"context"
	"fmt"
	"io"
	"sort"
	"sync"

	"novastream/internal/usenet"

	"github.com/javi11/nntppool/v2"
)

const (
	sevenZipRangePageSize   int64 = 1 << 20 // 1 MiB
	sevenZipMinCacheSizeMB        = 8
	sevenZipMaxCacheSizeMB        = 16
	sevenZipMaxRangeWorkers       = 4
)

type sevenZipRangePage struct {
	index int64
	data  []byte
}

// multiPart7zReader presents split 7z volumes as one ReaderAt. It fetches only
// the pages requested by the 7z header parser and retains a strictly bounded
// LRU, so memory use is independent of the archive's payload size.
type multiPart7zReader struct {
	ctx         context.Context
	cp          nntppool.UsenetConnectionPool
	sortFiles   []ParsedFile
	partOffsets []int64
	totalSize   int64
	workers     int
	pageSize    int64
	maxPages    int

	mu    sync.Mutex
	pages map[int64]*list.Element
	lru   *list.List

	// readPartAtFn is set only by unit tests to exercise multipart mapping and
	// cache eviction without an NNTP server.
	readPartAtFn func(part *ParsedFile, p []byte, off int64) (int, error)
}

func newMultiPart7zReader(
	ctx context.Context,
	cp nntppool.UsenetConnectionPool,
	files []ParsedFile,
	maxWorkers int,
	maxCacheSizeMB int,
) (*multiPart7zReader, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cp == nil {
		return nil, fmt.Errorf("usenet connection pool is required")
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("at least one 7z part is required")
	}

	copied := append([]ParsedFile(nil), files...)
	offsets := make([]int64, len(copied))
	var total int64
	for i := range copied {
		if copied[i].Size < 0 || total > int64(^uint64(0)>>1)-copied[i].Size {
			return nil, fmt.Errorf("invalid 7z part size for %s", copied[i].Filename)
		}
		offsets[i] = total
		total += copied[i].Size
	}
	if total == 0 {
		return nil, fmt.Errorf("7z archive is empty")
	}

	workers := maxWorkers
	if workers < 1 {
		workers = 1
	}
	if workers > sevenZipMaxRangeWorkers {
		workers = sevenZipMaxRangeWorkers
	}
	cacheMB := maxCacheSizeMB
	if cacheMB < sevenZipMinCacheSizeMB {
		cacheMB = sevenZipMinCacheSizeMB
	}
	if cacheMB > sevenZipMaxCacheSizeMB {
		cacheMB = sevenZipMaxCacheSizeMB
	}
	maxPages := int((int64(cacheMB) << 20) / sevenZipRangePageSize)
	if maxPages < 1 {
		maxPages = 1
	}

	return &multiPart7zReader{
		ctx:         ctx,
		cp:          cp,
		sortFiles:   copied,
		partOffsets: offsets,
		totalSize:   total,
		workers:     workers,
		pageSize:    sevenZipRangePageSize,
		maxPages:    maxPages,
		pages:       make(map[int64]*list.Element, maxPages),
		lru:         list.New(),
	}, nil
}

func (r *multiPart7zReader) cacheCapacityBytes() int64 {
	return int64(r.maxPages) * r.pageSize
}

func (r *multiPart7zReader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, fmt.Errorf("negative 7z read offset: %d", off)
	}
	if off >= r.totalSize {
		return 0, io.EOF
	}

	want := int64(len(p))
	if available := r.totalSize - off; want > available {
		want = available
	}
	written := 0
	for int64(written) < want {
		pageIndex := (off + int64(written)) / r.pageSize
		page, err := r.page(pageIndex)
		if err != nil {
			return written, err
		}
		pageStart := pageIndex * r.pageSize
		offsetInPage := int(off + int64(written) - pageStart)
		toCopy := len(page) - offsetInPage
		if remaining := int(want) - written; toCopy > remaining {
			toCopy = remaining
		}
		if toCopy <= 0 {
			return written, io.ErrUnexpectedEOF
		}
		copy(p[written:written+toCopy], page[offsetInPage:offsetInPage+toCopy])
		written += toCopy
	}

	if written < len(p) {
		return written, io.EOF
	}
	return written, nil
}

func (r *multiPart7zReader) page(index int64) ([]byte, error) {
	r.mu.Lock()
	if element := r.pages[index]; element != nil {
		r.lru.MoveToFront(element)
		data := element.Value.(*sevenZipRangePage).data
		r.mu.Unlock()
		return data, nil
	}
	r.mu.Unlock()

	start := index * r.pageSize
	end := start + r.pageSize
	if end > r.totalSize {
		end = r.totalSize
	}
	data := make([]byte, int(end-start))
	if _, err := r.readRawAt(data, start); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if existing := r.pages[index]; existing != nil {
		r.lru.MoveToFront(existing)
		return existing.Value.(*sevenZipRangePage).data, nil
	}
	element := r.lru.PushFront(&sevenZipRangePage{index: index, data: data})
	r.pages[index] = element
	for r.lru.Len() > r.maxPages {
		oldest := r.lru.Back()
		page := oldest.Value.(*sevenZipRangePage)
		delete(r.pages, page.index)
		r.lru.Remove(oldest)
	}
	return data, nil
}

func (r *multiPart7zReader) readRawAt(p []byte, off int64) (int, error) {
	written := 0
	for written < len(p) {
		absolute := off + int64(written)
		partIndex := r.findPartForOffset(absolute)
		if partIndex < 0 {
			return written, io.EOF
		}
		part := &r.sortFiles[partIndex]
		partStart := r.partOffsets[partIndex]
		partEnd := partStart + part.Size
		toRead := len(p) - written
		if available := partEnd - absolute; int64(toRead) > available {
			toRead = int(available)
		}
		if toRead <= 0 {
			return written, io.ErrUnexpectedEOF
		}

		var n int
		var err error
		if r.readPartAtFn != nil {
			n, err = r.readPartAtFn(part, p[written:written+toRead], absolute-partStart)
		} else {
			n, err = r.readPartAt(part, p[written:written+toRead], absolute-partStart)
		}
		written += n
		if err != nil {
			return written, err
		}
		if n != toRead {
			return written, io.ErrUnexpectedEOF
		}
	}
	return written, nil
}

func (r *multiPart7zReader) readPartAt(part *ParsedFile, p []byte, off int64) (int, error) {
	loader := parallelDbSegmentLoader{segs: part.Segments}
	rg := usenet.GetSegmentsInRange(off, off+int64(len(p))-1, loader)
	reader, err := usenet.NewUsenetReaderWithActivity(
		r.ctx,
		r.cp,
		rg,
		r.workers,
		"7z header: "+part.Filename,
	)
	if err != nil {
		return 0, fmt.Errorf("open 7z range in %s: %w", part.Filename, err)
	}
	defer reader.Close()

	n, err := io.ReadFull(reader, p)
	if err != nil {
		return n, fmt.Errorf("read 7z range in %s at %d: %w", part.Filename, off, err)
	}
	return n, nil
}

func (r *multiPart7zReader) findPartForOffset(off int64) int {
	if off < 0 || off >= r.totalSize {
		return -1
	}
	index := sort.Search(len(r.partOffsets), func(i int) bool {
		return r.partOffsets[i] > off
	}) - 1
	if index < 0 || off >= r.partOffsets[index]+r.sortFiles[index].Size {
		return -1
	}
	return index
}
