package usenet

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/acomagu/bufpipe"
	"github.com/mnightingale/rapidyenc"

	metapb "novastream/internal/nzb/metadata/proto"
)

// skipReader wraps an io.Reader and skips the first N bytes
type skipReader struct {
	reader    io.Reader
	skipBytes int64
	skipped   bool
}

func (sr *skipReader) Read(p []byte) (n int, err error) {
	if !sr.skipped {
		// Skip the required bytes first
		skipped, err := io.CopyN(io.Discard, sr.reader, sr.skipBytes)
		if err != nil && err != io.EOF {
			slog.Default().Warn("skipReader: skip operation failed",
				"requested_skip", sr.skipBytes,
				"actual_skipped", skipped,
				"error", err,
			)
			return 0, fmt.Errorf("skip failed after %d/%d bytes: %w", skipped, sr.skipBytes, err)
		}
		if skipped < sr.skipBytes {
			slog.Default().Warn("skipReader: insufficient data to skip",
				"requested_skip", sr.skipBytes,
				"actual_skipped", skipped,
			)
			// If we hit EOF before skipping enough, there's no more data to read
			return 0, io.EOF
		}
		sr.skipped = true
	}

	// Now read normally
	return sr.reader.Read(p)
}

type Segment struct {
	Id    string
	Start int64
	Size  int64 // Size of the segment in bytes
}

const nntpBufferLimitBytes int64 = 2 * 1024 * 1024

var (
	ErrBufferNotReady = errors.New("buffer not ready")
	ErrSegmentLimit   = errors.New("segment limit reached")
)

type segmentRange struct {
	start    int64
	end      int64
	segments []*segment
	current  int
}

func (r segmentRange) Get() (*segment, error) {
	if r.current >= len(r.segments) {
		return nil, ErrSegmentLimit
	}

	return r.segments[r.current], nil
}

func (r *segmentRange) Next() (*segment, error) {
	if r.current >= len(r.segments) {
		return nil, ErrSegmentLimit
	}

	// Ignore close errors
	_ = r.segments[r.current].Close()

	r.current++

	return r.Get()
}

func (r segmentRange) Segments() []*segment {
	return r.segments
}

func (r *segmentRange) Clear() error {
	segmentCount := len(r.segments)
	var totalFreedMemory int64

	for _, s := range r.segments {
		totalFreedMemory += s.SegmentSize
		if err := s.Close(); err != nil {
			slog.Warn("usenet.segment.cleanup_error",
				"segment_id", s.Id,
				"error", err,
			)
			return err
		}
	}

	slog.Debug("usenet.segment_range.cleared",
		"segments_freed", segmentCount,
	)

	r.segments = nil

	return nil
}

type segment struct {
	Id            string
	Start         int64
	End           int64
	SegmentSize   int64
	groups        []string
	reader        *bufpipe.PipeReader
	writer        *bufpipe.PipeWriter
	once          sync.Once
	mx            sync.Mutex
	bytesRead     int64
	fetchLogged   int32
	limited       io.Reader
	boundBytes    int64
	decoder       *rapidyenc.Decoder
	maxReadWindow int64
	consumedAt    int64
	requiredSince int64
	closed        int32
}

// Required state covers one consumer read, including decoder initialization.
// Time spent prefetched or between reads is not playback starvation.
func (s *segment) markRequired() {
	atomic.StoreInt64(&s.requiredSince, time.Now().UnixNano())
}

func (s *segment) clearRequired() {
	atomic.StoreInt64(&s.requiredSince, 0)
}

func (s *segment) isRequired() bool {
	return atomic.LoadInt32(&s.closed) == 0 && atomic.LoadInt64(&s.requiredSince) != 0
}

func (s *segment) requiredStalled(now time.Time, lastProgress int64, timeout time.Duration) bool {
	since := atomic.LoadInt64(&s.requiredSince)
	if since == 0 || atomic.LoadInt32(&s.closed) != 0 {
		return false
	}
	if since > lastProgress {
		lastProgress = since
	}
	return now.Sub(time.Unix(0, lastProgress)) >= timeout
}

func (s *segment) GetReader() io.Reader {
	s.once.Do(func() {
		// Create a bounded reader that enforces segment boundaries
		// This ensures we only read the exact bytes needed for this segment
		initialWindow := s.End - s.Start + 1
		if initialWindow < 0 {
			initialWindow = 0
		}
		if s.maxReadWindow == 0 {
			s.maxReadWindow = initialWindow
		}
		s.boundBytes = initialWindow

		var baseReader io.Reader = s.reader
		if s.reader != nil {
			bufReader := bufio.NewReader(baseReader)
			if isYEncStream(bufReader) {
				decoder := rapidyenc.AcquireDecoder(bufReader)
				s.decoder = decoder
				baseReader = decoder
			} else {
				baseReader = bufReader
			}
		}

		if s.decoder != nil {
			if partSize := s.decoder.Meta.PartSize; partSize > 0 {
				if s.SegmentSize > 0 && s.SegmentSize != partSize {
					slog.Default().Debug("usenet.segment.part_size_detected",
						"segment_id", s.Id,
						"metadata_size", s.SegmentSize,
						"yenc_part_size", partSize,
					)
				}
				s.SegmentSize = partSize
				maxReadable := partSize - s.Start
				if maxReadable < 0 {
					maxReadable = 0
				}
				if maxReadable > 0 && maxReadable < s.boundBytes {
					oldEnd := s.End
					s.boundBytes = maxReadable
					s.End = s.Start + maxReadable - 1
					slog.Default().Debug("usenet.segment.window_adjusted",
						"segment_id", s.Id,
						"start", s.Start,
						"old_end", oldEnd,
						"new_end", s.End,
						"metadata_window", initialWindow,
						"adjusted_window", s.boundBytes,
						"part_size", partSize,
					)
				}
			} else if s.SegmentSize > 0 && s.Start == 0 && s.SegmentSize != s.boundBytes {
				// Only adjust boundBytes to SegmentSize if we're reading the entire segment (Start=0)
				// Otherwise, respect the explicitly requested Start/End range
				s.boundBytes = s.SegmentSize
				oldEnd := s.End
				s.End = s.Start + s.boundBytes - 1
				slog.Default().Debug("usenet.segment.window_adjusted",
					"segment_id", s.Id,
					"start", s.Start,
					"old_end", oldEnd,
					"new_end", s.End,
					"metadata_window", initialWindow,
					"adjusted_window", s.boundBytes,
				)
			}
		}

		if s.Start > 0 {
			baseReader = &skipReader{reader: baseReader, skipBytes: s.Start}
		}

		if s.boundBytes < 0 {
			s.boundBytes = 0
		}

		// Limit reads to the decoded byte window for this segment.
		s.limited = io.LimitReader(baseReader, s.boundBytes)

		slog.Default().Debug("usenet.segment.reader_initialized",
			"segment_id", s.Id,
			"start", s.Start,
			"end", s.End,
			"bound_bytes", s.boundBytes,
			"has_decoder", s.decoder != nil,
			"segment_size", s.SegmentSize,
			"initial_window", initialWindow,
		)
	})

	return s.limited
}

func (s *segment) Length() int64 {
	return s.End - s.Start + 1
}

func (s *segment) adjustToBytesRead(total int64) {
	if total < 0 {
		total = 0
	}

	s.mx.Lock()
	defer s.mx.Unlock()

	s.boundBytes = total
	s.maxReadWindow = total

	if total == 0 {
		s.End = s.Start - 1
	} else {
		s.End = s.Start + total - 1
	}

	if s.decoder != nil && s.decoder.Meta.PartSize > 0 {
		s.SegmentSize = s.decoder.Meta.PartSize
	} else {
		s.SegmentSize = total
	}

	s.boundBytes = total

	slog.Default().Debug("usenet.segment.adjusted_to_bytes",
		"segment_id", s.Id,
		"total_bytes", total,
		"start", s.Start,
		"end", s.End,
	)
}

func (s *segment) addBytesRead(n int) {
	if n <= 0 {
		return
	}
	atomic.AddInt64(&s.bytesRead, int64(n))

	slog.Default().Debug("usenet.segment.bytes_read_accumulate",
		"segment_id", s.Id,
		"delta", n,
		"total", atomic.LoadInt64(&s.bytesRead),
		"expected", s.Length(),
	)
}

func (s *segment) BytesRead() int64 {
	return atomic.LoadInt64(&s.bytesRead)
}

// IsComplete returns true if the segment has been read completely
func (s *segment) IsComplete() bool {
	bytesRead := atomic.LoadInt64(&s.bytesRead)
	expected := s.Length()
	return bytesRead >= expected
}

// IsIncomplete returns true if the segment is partially read and could cause corruption
func (s *segment) IsIncomplete() bool {
	bytesRead := atomic.LoadInt64(&s.bytesRead)
	expected := s.Length()
	if expected <= 0 || bytesRead <= 0 {
		return false
	}

	return bytesRead < expected
}

func (s *segment) HitNNTPBufferLimit() bool {
	return s.hitNNTPBufferLimit(atomic.LoadInt64(&s.bytesRead))
}

func (s *segment) hitNNTPBufferLimit(bytesRead int64) bool {
	if bytesRead <= 0 {
		return false
	}

	expected := s.Length()
	if bytesRead >= expected {
		return false
	}

	totalRead := bytesRead + s.Start

	if totalRead < nntpBufferLimitBytes {
		return false
	}

	return s.Start+expected > nntpBufferLimitBytes
}

func (s *segment) BoundBytes() int64 {
	return atomic.LoadInt64(&s.boundBytes)
}

func (s *segment) shouldLogFetch() bool {
	return atomic.CompareAndSwapInt32(&s.fetchLogged, 0, 1)
}

func (s *segment) Close() error {
	atomic.StoreInt32(&s.closed, 1)
	s.clearRequired()
	s.mx.Lock()
	defer s.mx.Unlock()

	var e1, e2 error

	if s.decoder != nil {
		rapidyenc.ReleaseDecoder(s.decoder)
		s.decoder = nil
	}

	if s.reader != nil {
		e1 = s.reader.Close()
		s.reader = nil
	}

	if s.writer != nil {
		e2 = s.writer.Close()
		s.writer = nil
	}

	return errors.Join(e1, e2)
}

func (s *segment) Writer() *bufpipe.PipeWriter {
	s.mx.Lock()
	defer s.mx.Unlock()

	return s.writer
}

func (s *segment) ID() string {
	return s.Id
}

func (s *segment) Groups() []string {
	return s.groups
}

// BuildSegmentRange constructs a segmentRange covering the requested byte window.
// For large files, this may create thousands of segments causing memory issues.
func BuildSegmentRange(meta *metapb.FileMetadata, start, end int64) (segmentRange, error) {
	return buildSegmentRangeWithLimit(meta, start, end, 0) // 0 = no limit
}

// BuildSegmentRangeWithLimit constructs a segmentRange with a maximum segment limit
// to prevent memory explosion on large files.
func BuildSegmentRangeWithLimit(meta *metapb.FileMetadata, start, end int64, maxSegments int) (segmentRange, error) {
	return buildSegmentRangeWithLimit(meta, start, end, maxSegments)
}

// buildSegmentRangeWithLimit is the internal implementation that supports segment limiting
func buildSegmentRangeWithLimit(meta *metapb.FileMetadata, start, end int64, maxSegments int) (segmentRange, error) {
	sr := segmentRange{start: start, end: end}
	if meta == nil {
		return sr, ErrSegmentLimit
	}

	if end < 0 || end >= meta.FileSize {
		end = meta.FileSize - 1
	}

	slog.Debug("usenet.build_segment_range.request",
		"requested_start", start,
		"requested_end", end,
		"file_size", meta.FileSize,
		"num_segments", len(meta.SegmentData),
	)

	// Parse source NZB to get groups for segments
	groupsMap, err := parseNZBGroups(meta.SourceNzbPath)
	if err != nil {
		// Log error but continue - we'll try without groups (may fail at fetch time)
		slog.Warn("failed to parse NZB for groups", "nzb_path", meta.SourceNzbPath, "error", err)
		groupsMap = make(map[string][]string)
	}

	var (
		currentOffset int64
		segmentsBuilt int
	)

	// Ensure segments are in ascending logical order by StartOffset
	segments := append([]*metapb.SegmentData(nil), meta.SegmentData...)
	sort.Slice(segments, func(i, j int) bool {
		return segments[i].StartOffset < segments[j].StartOffset
	})

	for _, seg := range segments {
		// Check if this segment has pre-calculated boundaries
		hasPreCalculatedBounds := seg.StartOffset > 0 || seg.EndOffset > 0

		var segSize int64
		if hasPreCalculatedBounds {
			// Use pre-calculated decoded size
			segSize = seg.EndOffset - seg.StartOffset + 1
		} else {
			// No boundaries - estimate from encoded size with ~3% overhead reduction
			// This is approximate; actual size determined by yEnc decoder at runtime
			segSize = (seg.SegmentSize * 97) / 100
		}

		if segSize <= 0 {
			// Fallback to encoded size if calculation fails
			segSize = seg.SegmentSize
		}
		if segSize <= 0 {
			continue
		}

		segmentStart := currentOffset
		segmentEnd := currentOffset + segSize - 1

		// Skip segments entirely before the requested start
		if start > segmentEnd {
			currentOffset += segSize
			continue
		}

		startOffset := int64(0)
		if start > segmentStart {
			startOffset = start - segmentStart
		}

		endOffset := segSize - 1
		if end >= 0 && end < segmentEnd {
			endOffset = end - segmentStart
		}

		// Log segment details for debugging
		if startOffset > 0 || segmentEnd >= end {
			slog.Debug("usenet.build_segment_range.segment_details",
				"segment_id", seg.Id,
				"has_precalc_bounds", hasPreCalculatedBounds,
				"encoded_size", seg.SegmentSize,
				"estimated_decoded_size", segSize,
				"segment_start_in_file", segmentStart,
				"segment_end_in_file", segmentEnd,
				"start_offset_in_segment", startOffset,
				"end_offset_in_segment", endOffset,
				"bytes_to_read", endOffset-startOffset+1,
			)
		}

		// Get groups for this segment ID
		groups := groupsMap[seg.Id]

		reader, writer := bufpipe.New(nil)
		s := &segment{
			Id:          seg.Id,
			Start:       startOffset,
			End:         endOffset,
			SegmentSize: segSize,
			groups:      groups,
			reader:      reader,
			writer:      writer,
		}

		sr.segments = append(sr.segments, s)
		segmentsBuilt++

		// CRITICAL FIX: Limit segments to prevent memory explosion
		if maxSegments > 0 && segmentsBuilt >= maxSegments {
			slog.Debug("usenet.build_segment_range.segment_limit_reached",
				"max_segments", maxSegments,
				"segments_built", segmentsBuilt,
			)
			break
		}

		if end >= 0 && segmentEnd >= end {
			break
		}

		currentOffset += segSize
	}

	if segmentsBuilt == 0 {
		return sr, ErrSegmentLimit
	}

	// Calculate total estimated memory usage
	var totalEstimatedMemory int64
	for _, seg := range sr.segments {
		totalEstimatedMemory += seg.SegmentSize
	}

	slog.Debug("usenet.build_segment_range.result",
		"requested_start", start,
		"requested_end", end,
		"segments_built", segmentsBuilt,
	)

	return sr, nil
}

func SegmentRangeCount(r segmentRange) int {
	return len(r.segments)
}

// parseNZBGroups reads an NZB file and returns a map of segment ID to newsgroups
func parseNZBGroups(nzbPath string) (map[string][]string, error) {
	if nzbPath == "" {
		return nil, fmt.Errorf("empty NZB path")
	}

	data, err := os.ReadFile(nzbPath)
	if err != nil {
		return nil, fmt.Errorf("read NZB file: %w", err)
	}

	type nzbGroup struct {
		Name string `xml:",chardata"`
	}

	type nzbSegment struct {
		ID     string     `xml:",chardata"`
		Groups []nzbGroup `xml:"-"`
	}

	type nzbFile struct {
		Groups   []nzbGroup   `xml:"groups>group"`
		Segments []nzbSegment `xml:"segments>segment"`
	}

	type nzbRoot struct {
		Files []nzbFile `xml:"file"`
	}

	var root nzbRoot
	if err := xml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse NZB XML: %w", err)
	}

	groupsMap := make(map[string][]string)
	for _, file := range root.Files {
		// Convert nzbGroup slice to string slice
		fileGroups := make([]string, 0, len(file.Groups))
		for _, g := range file.Groups {
			if trimmed := strings.TrimSpace(g.Name); trimmed != "" {
				fileGroups = append(fileGroups, trimmed)
			}
		}

		// Assign groups to each segment in this file
		for _, seg := range file.Segments {
			if trimmedID := strings.TrimSpace(seg.ID); trimmedID != "" {
				groupsMap[trimmedID] = fileGroups
			}
		}
	}

	return groupsMap, nil
}

func isYEncStream(r *bufio.Reader) bool {
	const yEncPrefix = "=ybegin"

	peek, err := r.Peek(len(yEncPrefix))
	if err != nil {
		return false
	}

	return bytes.Equal(peek[:len(yEncPrefix)], []byte(yEncPrefix))
}
