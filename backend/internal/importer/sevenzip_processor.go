package importer

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	metapb "novastream/internal/nzb/metadata/proto"
	"novastream/internal/pool"

	"github.com/javi11/nntppool/v2"
)

// SevenZipDiscoveryCallback is called when a file is discovered during progressive 7z analysis.
// Return true to continue analysis, false to stop early.
type SevenZipDiscoveryCallback func(file sevenZipContent) bool

// SevenZipProcessor interface for analyzing 7z content from NZB data
type SevenZipProcessor interface {
	// Analyze7zContentFromNzb analyzes a 7z archive directly from NZB data
	// without downloading. Returns an array of sevenZipContent with file metadata and segments.
	// Only supports uncompressed (store mode) 7z archives.
	Analyze7zContentFromNzb(ctx context.Context, szFiles []ParsedFile) ([]sevenZipContent, error)
	// Analyze7zContentFromNzbProgressive analyzes a 7z archive progressively, calling
	// the callback for each file discovered. This allows for early playback of the first
	// video file while analysis continues in the background.
	Analyze7zContentFromNzbProgressive(ctx context.Context, szFiles []ParsedFile, callback SevenZipDiscoveryCallback) ([]sevenZipContent, error)
	// CreateFileMetadataFrom7zContent creates FileMetadata from sevenZipContent for the metadata
	// system. This is used to convert sevenZipContent into the protobuf format used by the metadata system.
	CreateFileMetadataFrom7zContent(content sevenZipContent, sourceNzbPath string) *metapb.FileMetadata
}

// sevenZipContent represents a file within a 7z archive for processing
type sevenZipContent struct {
	InternalPath string                `json:"internal_path"`
	Filename     string                `json:"filename"`
	Size         int64                 `json:"size"`
	Segments     []*metapb.SegmentData `json:"segments"`               // Segment data for this file
	AesKey       []byte                `json:"-"`                      // Derived 7z AES key; never persist the archive password
	AesIV        []byte                `json:"-"`                      // Per-file AES-CBC IV
	IsDirectory  bool                  `json:"is_directory,omitempty"` // Indicates if this is a directory
}

// sevenZipProcessor handles 7z archive analysis and content extraction
type sevenZipProcessor struct {
	log            *slog.Logger
	poolManager    pool.Manager
	maxWorkers     int
	maxCacheSizeMB int
}

// NewSevenZipProcessor creates a new 7z processor
func NewSevenZipProcessor(poolManager pool.Manager, maxWorkers int, maxCacheSizeMB int) SevenZipProcessor {
	return &sevenZipProcessor{
		log:            slog.Default().With("component", "7z-processor"),
		poolManager:    poolManager,
		maxWorkers:     maxWorkers,
		maxCacheSizeMB: maxCacheSizeMB,
	}
}

// NewSevenZipProcessorWithConfig creates a new 7z processor with memory preloading configuration
func NewSevenZipProcessorWithConfig(poolManager pool.Manager, maxWorkers int, maxCacheSizeMB int, enableMemoryPreload bool, maxMemoryGB int) SevenZipProcessor {
	// 7z analysis is always range-backed. Keep these legacy arguments for
	// configuration compatibility, but never use them to preload an archive.
	_ = enableMemoryPreload
	_ = maxMemoryGB
	return &sevenZipProcessor{
		log:            slog.Default().With("component", "7z-processor"),
		poolManager:    poolManager,
		maxWorkers:     maxWorkers,
		maxCacheSizeMB: maxCacheSizeMB,
	}
}

// CreateFileMetadataFrom7zContent creates FileMetadata from sevenZipContent for the metadata system
func (sp *sevenZipProcessor) CreateFileMetadataFrom7zContent(
	content sevenZipContent,
	sourceNzbPath string,
) *metapb.FileMetadata {
	now := time.Now().Unix()

	meta := &metapb.FileMetadata{
		FileSize:      content.Size,
		SourceNzbPath: sourceNzbPath,
		Status:        metapb.FileStatus_FILE_STATUS_HEALTHY,
		CreatedAt:     now,
		ModifiedAt:    now,
		SegmentData:   content.Segments,
	}
	if len(content.AesKey) > 0 {
		meta.Encryption = metapb.Encryption_HEADERS
		meta.Password = base64.StdEncoding.EncodeToString(content.AesKey)
		meta.Salt = base64.StdEncoding.EncodeToString(content.AesIV)
	}
	return meta
}

// Analyze7zContentFromNzb analyzes a 7z archive directly from NZB data without downloading
// This implementation streams the 7z header from Usenet and parses it to extract file metadata
// Only supports uncompressed (store mode) 7z archives
func (sp *sevenZipProcessor) Analyze7zContentFromNzb(ctx context.Context, szFiles []ParsedFile) ([]sevenZipContent, error) {
	if sp.poolManager == nil {
		return nil, NewNonRetryableError("no pool manager available", nil)
	}

	// Rename and sort 7z files
	sortFiles := rename7zFilesAndSort(szFiles)
	if len(sortFiles) == 0 {
		return nil, NewNonRetryableError("no 7z files to process", nil)
	}

	cp, err := sp.poolManager.GetPool()
	if err != nil {
		return nil, NewNonRetryableError("no connection pool available", err)
	}

	// Extract filenames for first part detection
	fileNames := make([]string, len(sortFiles))
	for i, file := range sortFiles {
		fileNames[i] = file.Filename
	}

	// Find the first 7z part
	main7zFile, err := getFirst7zPart(fileNames)
	if err != nil {
		return nil, err
	}

	sp.log.Info("Starting 7z analysis",
		"main_file", main7zFile,
		"total_parts", len(sortFiles),
		"sz_files", len(szFiles),
		"access_mode", "bounded-range")

	// Calculate total size
	var totalSize int64
	for _, f := range sortFiles {
		totalSize += f.Size
	}

	// 7z stores the location of its file table in the signature header. Analyze it
	// through bounded ReaderAt requests instead of downloading the media payload.
	return sp.analyze7zWithStreaming(ctx, cp, sortFiles, main7zFile, totalSize)
}

// Analyze7zContentFromNzbProgressive analyzes a 7z archive progressively with callbacks
func (sp *sevenZipProcessor) Analyze7zContentFromNzbProgressive(ctx context.Context, szFiles []ParsedFile, callback SevenZipDiscoveryCallback) ([]sevenZipContent, error) {
	// For 7z, we parse all headers at once (they're at the end of the archive)
	// Then call the callback for each discovered file
	contents, err := sp.Analyze7zContentFromNzb(ctx, szFiles)
	if err != nil {
		return nil, err
	}

	// Call callback for each file progressively
	result := make([]sevenZipContent, 0, len(contents))
	for _, content := range contents {
		result = append(result, content)

		if callback != nil {
			shouldContinue := callback(content)
			if !shouldContinue {
				sp.log.Info("Progressive 7z analysis stopped early by callback",
					"files_discovered", len(result),
					"total_files", len(contents))
				return result, nil
			}
		}
	}

	return result, nil
}

// analyze7zWithStreaming analyzes 7z archive by streaming from usenet
func (sp *sevenZipProcessor) analyze7zWithStreaming(ctx context.Context, cp nntppool.UsenetConnectionPool, sortFiles []ParsedFile, main7zFile string, totalSize int64) ([]sevenZipContent, error) {
	sp.log.Info("Using bounded range approach for 7z analysis")

	reader, size, err := sp.createUsenetMultiPartReader(ctx, cp, sortFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to create multi-part reader: %w", err)
	}

	// Parse 7z headers
	analysisStart := time.Now()
	archiveInfo, err := parse7zHeaders(reader, size, archivePassword(sortFiles))
	if err != nil {
		return nil, err
	}

	analysisDuration := time.Since(analysisStart)

	sp.log.Info("Successfully analyzed 7z archive via bounded ranges",
		"main_file", main7zFile,
		"files_found", len(archiveInfo.Files),
		"analysis_duration", analysisDuration,
		"is_uncompressed", archiveInfo.IsUncompressed,
		"range_cache_bytes", reader.cacheCapacityBytes())

	// Convert to sevenZipContent with segment mapping
	return sp.convertFilesToContent(archiveInfo.Files, sortFiles)
}

// createUsenetMultiPartReader creates a reader that can read across multiple 7z parts from usenet
func (sp *sevenZipProcessor) createUsenetMultiPartReader(ctx context.Context, cp nntppool.UsenetConnectionPool, sortFiles []ParsedFile) (*multiPart7zReader, int64, error) {
	reader, err := newMultiPart7zReader(ctx, cp, sortFiles, sp.maxWorkers, sp.maxCacheSizeMB)
	if err != nil {
		return nil, 0, err
	}
	return reader, reader.totalSize, nil
}

// szPartInfo holds information about a 7z archive part for segment mapping
type szPartInfo struct {
	file     *ParsedFile
	startOff int64 // Start offset in combined archive
	endOff   int64 // End offset in combined archive
}

// convertFilesToContent converts 7z file entries to sevenZipContent with segment mapping
func (sp *sevenZipProcessor) convertFilesToContent(files []sevenZipFileEntry, sortFiles []ParsedFile) ([]sevenZipContent, error) {
	contents := make([]sevenZipContent, 0, len(files))

	// Build segment data for the combined archive
	// All parts are concatenated, so we need to track absolute offsets
	parts := make([]szPartInfo, len(sortFiles))
	var currentOff int64
	for i := range sortFiles {
		parts[i] = szPartInfo{
			file:     &sortFiles[i],
			startOff: currentOff,
			endOff:   currentOff + sortFiles[i].Size,
		}
		currentOff += sortFiles[i].Size
	}

	for _, entry := range files {
		if entry.IsDirectory {
			continue
		}

		// Skip non-video/audio files for efficiency
		ext := strings.ToLower(filepath.Ext(entry.Name))
		if !isMediaFile(ext) {
			sp.log.Debug("Skipping non-media file in 7z",
				"file", entry.Name,
				"ext", ext)
			continue
		}

		// Map the file's byte range to segments
		packedSize := entry.PackedSize
		if packedSize <= 0 {
			packedSize = entry.UncompressedSize
		}
		segments, err := sp.mapFileToSegments(entry.PackedOffset, packedSize, parts)
		if err != nil {
			sp.log.Warn("Failed to map 7z file to segments",
				"file", entry.Name,
				"offset", entry.PackedOffset,
				"size", entry.UncompressedSize,
				"error", err)
			continue
		}

		content := sevenZipContent{
			InternalPath: entry.Name,
			Filename:     filepath.Base(entry.Name),
			Size:         entry.UncompressedSize,
			Segments:     segments,
			AesKey:       append([]byte(nil), entry.AesKey...),
			AesIV:        append([]byte(nil), entry.AesIV...),
			IsDirectory:  false,
		}
		contents = append(contents, content)
	}

	return contents, nil
}

// isMediaFile checks if the extension represents a media file
func isMediaFile(ext string) bool {
	mediaExts := map[string]bool{
		".mkv": true, ".mp4": true, ".avi": true, ".mov": true,
		".wmv": true, ".flv": true, ".webm": true, ".m4v": true,
		".mpg": true, ".mpeg": true, ".ts": true, ".m2ts": true,
		".mp3": true, ".flac": true, ".aac": true, ".ogg": true,
		".wav": true, ".wma": true, ".m4a": true,
		".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".idx": true,
	}
	return mediaExts[ext]
}

// mapFileToSegments maps a file's byte range within the archive to NZB segments
func (sp *sevenZipProcessor) mapFileToSegments(fileOffset, fileSize int64, parts []szPartInfo) ([]*metapb.SegmentData, error) {
	if fileSize <= 0 {
		return nil, nil
	}

	fileEnd := fileOffset + fileSize - 1
	var segments []*metapb.SegmentData

	for _, part := range parts {
		// Skip parts that don't overlap with the file
		if part.endOff <= fileOffset || part.startOff > fileEnd {
			continue
		}

		// Calculate the overlap region
		overlapStart := fileOffset
		if overlapStart < part.startOff {
			overlapStart = part.startOff
		}
		overlapEnd := fileEnd
		if overlapEnd >= part.endOff {
			overlapEnd = part.endOff - 1
		}

		// Convert to offsets within the part
		partDataOffset := overlapStart - part.startOff
		partDataSize := overlapEnd - overlapStart + 1

		// Slice the segments from this part
		sliced, covered, err := slicePartSegments(part.file.Segments, partDataOffset, partDataSize)
		if err != nil {
			sp.log.Warn("Failed slicing part segments",
				"error", err,
				"part", part.file.Filename,
				"offset", partDataOffset,
				"size", partDataSize)
			continue
		}

		if covered != partDataSize {
			sp.log.Debug("Part coverage mismatch",
				"part", part.file.Filename,
				"expected", partDataSize,
				"covered", covered)
		}

		segments = append(segments, sliced...)
	}

	return segments, nil
}
