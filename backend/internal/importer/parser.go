package importer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/javi11/nntppool/v2/pkg/nntpcli"
	"github.com/javi11/nzbparser"
	"golang.org/x/sync/errgroup"
	"novastream/internal/encryption"
	"novastream/internal/encryption/rclone"
	metapb "novastream/internal/nzb/metadata/proto"
	"novastream/internal/pool"
)

// minConcurrentNZBFileParsers is the floor for yEnc header-fetch concurrency in
// file parsers: a resolve always runs at least this many file parsers in
// parallel. Combined with parallelYEncFetchesPerParser, that floors the number
// of in-flight header fetches at the historical fixed width (4 file parsers x 1
// fetch), so we never regress below it. The pool backpressures the actual
// connection usage, so exceeding the floor is always safe.
const minConcurrentNZBFileParsers = 2

// parallelYEncFetchesPerParser is the worst-case number of concurrent yEnc
// header fetches a single file parser may issue: parseFileWithContext fetches
// the first and the final segment headers concurrently.
const parallelYEncFetchesPerParser = 2

// defaultMaxConcurrentNZBFileParsers is the hard cap on per-title yEnc header
// file-parser concurrency when settings don't override it. It bounds how many
// NNTP header round-trips a single resolve can issue at once, even when the pool
// has many free connections.
const defaultMaxConcurrentNZBFileParsers = 16

// defaultParserShareDivisor spreads a resolve's yEnc header fetches across 1/N
// of the pool's currently-free connections (1/4 by default) so that several
// concurrent requests can each parallelize without any one of them filling the
// pool.
const defaultParserShareDivisor = 4

// NzbType represents the type of NZB content
type NzbType string

const (
	NzbTypeSingleFile NzbType = "single_file"
	NzbTypeMultiFile  NzbType = "multi_file"
	NzbTypeRarArchive NzbType = "rar_archive"
	NzbType7zArchive  NzbType = "7z_archive"
	NzbTypeStrm       NzbType = "strm_file"
)

// ParsedNzb contains the parsed NZB data and extracted metadata
type ParsedNzb struct {
	Path          string
	Filename      string
	TotalSize     int64
	Type          NzbType
	Files         []ParsedFile
	SegmentsCount int
	SegmentSize   int64
	// Par2Files holds the PAR2 files that were filtered out of Files. They carry no
	// playable content but their FileDesc packets enumerate the recovery set, used for
	// completeness verification. Only identity fields (Filename/Segments/Groups) are
	// populated — these are not yEnc-normalized like Files.
	Par2Files []ParsedFile
}

// ParsedFile represents a file extracted from the NZB
type ParsedFile struct {
	Subject      string
	Filename     string
	Size         int64
	Segments     []*metapb.SegmentData
	Groups       []string
	IsRarArchive bool
	Is7zArchive  bool
	Encryption   metapb.Encryption // Encryption type (e.g., "rclone"), nil if not encrypted
	Password     string            // Password from NZB meta, nil if not encrypted
	Salt         string            // Salt from NZB meta, nil if not encrypted
}

var (
	// Pattern to detect RAR files
	rarPattern = regexp.MustCompile(`(?i)\.r(ar|\d+)$|\.part\d+\.rar$`)
	// Pattern to detect 7z files (including multipart .7z.001, .7z.002, etc.)
	sevenZipPattern = regexp.MustCompile(`(?i)\.7z(\.\d+)?$`)
	// Pattern to detect PAR2 files
	par2Pattern = regexp.MustCompile(`(?i)\.par2$|\.p\d+$|\.vol\d+\+\d+\.par2$`)
	// Pattern to detect optional recovery/parity volumes (PAR2 parity + RAR .rev recovery).
	// These exist only to repair damaged content; the main archive extracts without them.
	// If their articles are missing we can still play the content, so parse failures on
	// these files must never abort the whole import.
	// Matches a .par2 or .rev extension at a token boundary (end of string or
	// followed by a non-alphanumeric char) so it works on both bare filenames
	// ("Movie.part002.rev") and raw NZB subjects ("... \"Movie.part002.rev\" -").
	recoveryPattern = regexp.MustCompile(`(?i)\.(par2|rev)($|[^a-z0-9])`)
	// Matches a RAR volume in either a bare filename or a longer NZB subject. Unlike
	// rarPattern, this deliberately accepts a trailing quote/space so it can identify
	// subjects such as `"movie.part001.rar" yEnc (1/100)` before yEnc headers exist.
	rarVolumeTokenPattern = regexp.MustCompile(`(?i)(?:\.part\d+\.rar|\.rar|\.r\d+)($|[^a-z0-9])`)
)

var (
	// Matches each <file ...> opening tag in an NZB.
	nzbFileTagRE = regexp.MustCompile(`(?is)<file\b[^>]*>`)
	// Captures the value of the poster / subject attribute within a <file> tag.
	nzbPosterAttrRE  = regexp.MustCompile(`(?is)\bposter\s*=\s*"([^"]*)"`)
	nzbSubjectAttrRE = regexp.MustCompile(`(?is)\bsubject\s*=\s*"([^"]*)"`)
	// A poster value that actually carries the file description (yEnc part info and/or a
	// quoted filename), signalling a malformed NZB whose subject was left empty/absent.
	nzbPosterLooksLikeSubjectRE = regexp.MustCompile(`(?i)yenc|&quot;|"|\.par2|\.rar|\.rev`)
)

// normalizeNzbSubjects repairs malformed NZBs that store the file description (filename + yEnc
// part info) in the <file> element's poster attribute while leaving subject empty or absent.
// nzbparser derives filenames from subject AND de-duplicates files keyed on subject, so an
// empty subject both loses the filename and collapses every such file into a single unusable
// blob (all segments merged). When we detect a poster that looks like a real subject, we copy
// it into subject so each file parses with its correct, unique name. Standard NZBs (non-empty
// subject) are left untouched. Returns the possibly-rewritten bytes and the number of files fixed.
func normalizeNzbSubjects(raw []byte) ([]byte, int) {
	fixed := 0
	out := nzbFileTagRE.ReplaceAllFunc(raw, func(tag []byte) []byte {
		subjIdx := nzbSubjectAttrRE.FindSubmatchIndex(tag)
		if subjIdx != nil && len(bytes.TrimSpace(tag[subjIdx[2]:subjIdx[3]])) > 0 {
			return tag // standard NZB: subject already present
		}
		posterMatch := nzbPosterAttrRE.FindSubmatch(tag)
		if posterMatch == nil {
			return tag
		}
		poster := posterMatch[1]
		if len(bytes.TrimSpace(poster)) == 0 || !nzbPosterLooksLikeSubjectRE.Match(poster) {
			return tag // poster is an ordinary uploader id, not a file description
		}
		fixed++
		if subjIdx != nil {
			// Replace the empty subject="" value in place (avoids regexp $-expansion).
			newTag := make([]byte, 0, len(tag)+len(poster))
			newTag = append(newTag, tag[:subjIdx[2]]...)
			newTag = append(newTag, poster...)
			newTag = append(newTag, tag[subjIdx[3]:]...)
			return newTag
		}
		// No subject attribute at all — inject one right after "<file".
		const anchor = "<file"
		idx := bytes.Index(tag, []byte(anchor)) + len(anchor)
		newTag := make([]byte, 0, len(tag)+len(poster)+12)
		newTag = append(newTag, tag[:idx]...)
		newTag = append(newTag, ` subject="`...)
		newTag = append(newTag, poster...)
		newTag = append(newTag, '"')
		newTag = append(newTag, tag[idx:]...)
		return newTag
	})
	return out, fixed
}

// isRecoveryFile reports whether the given name (filename or NZB subject) refers to an
// optional recovery/parity volume that is not required to extract the main content.
func isRecoveryFile(names ...string) bool {
	for _, name := range names {
		if recoveryPattern.MatchString(strings.ToLower(strings.TrimSpace(name))) {
			return true
		}
	}
	return false
}

// requiresEncryptedRarVolume reports whether the file is a required volume in a
// password-protected RAR set. Encrypted archives cannot tolerate a ciphertext hole:
// without downloading and repairing the complete set, all later random-access
// offsets are unusable. Recovery volumes remain optional and are excluded here.
func requiresEncryptedRarVolume(meta map[string]string, names ...string) bool {
	if strings.TrimSpace(meta["password"]) == "" || isRecoveryFile(names...) {
		return false
	}
	for _, name := range names {
		if rarVolumeTokenPattern.MatchString(strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

// Parser handles NZB file parsing
type Parser struct {
	poolManager  pool.Manager  // Pool manager for dynamic pool access
	log          *slog.Logger  // Logger for debug/error messages
	deobfuscator *Deobfuscator // Filename deobfuscator

	// maxConcurrentFileParsers caps how many file parsers fetch yEnc headers in
	// parallel for a single title (0 = default). parserShareDivisor spreads them
	// across 1/N of the pool's free connections; <=1 disables the sharing
	// heuristic and uses the hard cap directly.
	maxConcurrentFileParsers int
	parserShareDivisor       int
}

// NewParser creates a new NZB parser
func NewParser(poolManager pool.Manager) *Parser {
	return &Parser{
		poolManager:              poolManager,
		log:                      slog.Default().With("component", "nzb-parser"),
		deobfuscator:             NewDeobfuscator(poolManager),
		maxConcurrentFileParsers: defaultMaxConcurrentNZBFileParsers,
		parserShareDivisor:       defaultParserShareDivisor,
	}
}

// SetConcurrency configures the per-title yEnc header-fetch parallelism.
// hardCap bounds how many file parsers run concurrently (0 leaves the default);
// shareDivisor spreads them across 1/N of the pool's free connections (<=1
// disables spreading and uses the hard cap alone).
func (p *Parser) SetConcurrency(hardCap, shareDivisor int) {
	if hardCap > 0 {
		p.maxConcurrentFileParsers = hardCap
	}
	if shareDivisor >= 0 {
		p.parserShareDivisor = shareDivisor
	}
}

// ParseFile parses an NZB file from a reader
func (p *Parser) ParseFile(r io.Reader, nzbPath string) (*ParsedNzb, error) {
	return p.ParseFileWithContext(context.Background(), r, nzbPath)
}

// ParseFileWithContext parses an NZB file from a reader with context support for cancellation
func (p *Parser) ParseFileWithContext(ctx context.Context, r io.Reader, nzbPath string) (*ParsedNzb, error) {
	// Check for context cancellation before starting
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, NewNonRetryableError("failed to read NZB", err)
	}
	// Repair malformed NZBs that carry filenames in the poster attribute with an empty
	// subject; left unfixed, nzbparser collapses all such files into one unusable blob.
	if normalized, repaired := normalizeNzbSubjects(raw); repaired > 0 {
		p.log.Info("repaired malformed NZB: copied poster attribute into empty subject",
			"files_repaired", repaired)
		raw = normalized
	}

	n, err := nzbparser.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, NewNonRetryableError("failed to parse NZB XML", err)
	}

	if len(n.Files) == 0 {
		return nil, NewNonRetryableError("NZB file contains no files", nil)
	}

	parsed := &ParsedNzb{
		Path:     nzbPath,
		Filename: filepath.Base(nzbPath),
		Files:    make([]ParsedFile, 0, len(n.Files)),
	}
	// Determine segment size from meta chunk_size or fallback to first segment size
	var segSize int64
	if n.Meta != nil {
		if v, ok := n.Meta["chunk_size"]; ok {
			if iv, err := strconv.ParseInt(v, 10, 64); err == nil && iv > 0 {
				segSize = iv
			}
		}
	}

	// Process each file in the NZB in parallel
	// Filter out PAR2 files first, but retain their identity for completeness checks.
	var validFiles []nzbparser.NzbFile
	for _, file := range n.Files {
		if par2Pattern.MatchString(file.Filename) {
			parsed.Par2Files = append(parsed.Par2Files, par2IdentityFile(file))
			continue
		}
		validFiles = append(validFiles, file)
	}

	if len(validFiles) == 0 {
		return nil, NewNonRetryableError("NZB file contains no valid files (only PAR2)", nil)
	}

	// Check for context cancellation before parallel processing
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Each file parser opens NNTP body readers to inspect the first and final
	// yEnc headers. The width is a fraction of the pool's free connections
	// (bounded by the configured hard cap) so several concurrent resolves share
	// the pool; the pool itself backpressures the actual connection usage, so
	// this width only limits how many header round-trips run in parallel.
	parsedFiles, err := runBoundedFileParsers(ctx, len(validFiles), p.fileParserLimit(), func(parseCtx context.Context, i int) (*ParsedFile, error) {
		file := validFiles[i]
		parsedFile, parseErr := p.parseFileWithContext(parseCtx, file, n.Meta, n.Files, parsed.Filename)
		if parseErr != nil {
			return nil, fmt.Errorf("failed to parse file %s: %w", file.Subject, parseErr)
		}
		return parsedFile, nil
	})
	if err != nil {
		return nil, err
	}

	// Aggregate results in the original order
	for _, parsedFile := range parsedFiles {
		parsed.Files = append(parsed.Files, *parsedFile)
		parsed.TotalSize += parsedFile.Size
		parsed.SegmentsCount += len(parsedFile.Segments)

		if len(parsedFile.Segments) > 0 {
			// Find the corresponding original file to check segment bytes
			for _, file := range validFiles {
				if file.Subject == parsedFile.Subject {
					if len(file.Segments) > 0 && file.Segments[0].Bytes > int(segSize) {
						// Fallback to the first segment size encountered
						segSize = int64(file.Segments[0].Bytes)
					}
					break
				}
			}
		}
	}

	parsed.SegmentSize = segSize

	// Determine NZB type based on content analysis
	parsed.Type = p.determineNzbType(parsed.Files)

	return parsed, nil
}

// fileParserLimit computes how many file parsers may run concurrently for a
// single title. It takes the configured hard cap, then — when the pool-sharing
// heuristic is enabled — narrows it so that the resulting in-flight yEnc header
// fetches never exceed 1/N of the pool's currently-free connections (each file
// parser can run first+last fetches in parallel, so the fetch budget is divided
// by parallelYEncFetchesPerParser). Several concurrent resolves can therefore
// each parallelize without one saturating the pool, and a constrained pool keeps
// the same in-flight fetch footprint as the historical fixed width. It always
// returns at least minConcurrentNZBFileParsers, even if the configured cap is
// lower, so a resolve never regresses below the historical fixed width.
func (p *Parser) fileParserLimit() int {
	limit := p.maxConcurrentFileParsers
	if limit <= 0 {
		limit = defaultMaxConcurrentNZBFileParsers
	}
	if p.parserShareDivisor > 1 && p.poolManager != nil {
		// Share 1/N of the connections that are free right now, expressed as an
		// in-flight header fetch budget, then convert back to file parsers. A 0
		// budget (pool saturated or absent) leaves the floor to apply below
		// rather than using the full cap against a busy pool.
		fetchBudget := p.poolManager.AvailableConnections() / p.parserShareDivisor
		if share := (fetchBudget + parallelYEncFetchesPerParser - 1) / parallelYEncFetchesPerParser; share < limit {
			limit = share
		}
	}
	if limit < minConcurrentNZBFileParsers {
		limit = minConcurrentNZBFileParsers
	}
	return limit
}

// runBoundedFileParsers preserves file order while canceling sibling probes as
// soon as one volume proves the release cannot be parsed. This prevents a bad
// multi-volume NZB from serially probing every remaining volume.
func runBoundedFileParsers(ctx context.Context, count, limit int, parse func(context.Context, int) (*ParsedFile, error)) ([]*ParsedFile, error) {
	group, parseCtx := errgroup.WithContext(ctx)
	group.SetLimit(limit)
	results := make([]*ParsedFile, count)

	for i := 0; i < count; i++ {
		if err := parseCtx.Err(); err != nil {
			break
		}
		i := i
		group.Go(func() error {
			parsedFile, err := parse(parseCtx, i)
			if err != nil {
				return err
			}
			results[i] = parsedFile
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// parseFile processes a single file entry from the NZB (legacy, no context)
func (p *Parser) parseFile(file nzbparser.NzbFile, meta map[string]string, allFiles []nzbparser.NzbFile, nzbFilename string) (*ParsedFile, error) {
	return p.parseFileWithContext(context.Background(), file, meta, allFiles, nzbFilename)
}

// parseFileWithContext processes a single file entry from the NZB with context support
func (p *Parser) parseFileWithContext(ctx context.Context, file nzbparser.NzbFile, meta map[string]string, allFiles []nzbparser.NzbFile, nzbFilename string) (*ParsedFile, error) {
	// Check for context cancellation before starting
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	sort.Sort(file.Segments)

	// Recovery/parity volumes (.par2, .rev) are optional repair data, not playable
	// content. Their articles are often the first to be DMCA'd or to fall out of
	// retention, so we must tolerate yEnc-header fetch failures on them and fall back
	// to the NZB-declared segment sizes rather than aborting the entire import.
	isRecovery := isRecoveryFile(file.Filename, file.Subject)

	// Fetch yEnc headers from the first segment to get correct filename and file size,
	// some nzbs have wrong filename in the segments. The first and (when sizing
	// normalization is needed) last segment headers are independent round-trips, so
	// they are fetched concurrently to halve this file's serialized header-fetch
	// wall-clock. firstPartSize is reused by normalizeSegmentSizesWithYenc so that
	// normalization need not re-fetch segment 0.
	var (
		yencFilename  string
		yencFileSize  int64
		firstPartSize int64
		lastPartSize  int64
		lastPartErr   error
	)
	usePool := p.poolManager != nil && p.poolManager.HasPool()
	// needLast is true exactly when normalization will run, i.e. there are enough
	// segments and this is not an optional recovery/parity volume.
	needLast := usePool && len(file.Segments) >= 2 && !isRecovery
	if usePool && len(file.Segments) > 0 {
		// Check for context cancellation before network call
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		var (
			firstPartHeaders nntpcli.YencHeaders
			firstErr         error
			wg               sync.WaitGroup
		)
		if needLast {
			wg.Add(1)
			go func() {
				defer wg.Done()
				lastPartSize, lastPartErr = p.lastSegmentPartSize(ctx, file.Segments)
			}()
		}
		firstPartHeaders, firstErr = p.fetchYencHeaders(ctx, file.Segments[0], nil)

		if firstErr != nil {
			// Terminal first-segment verdicts fail fast: the concurrent last-segment
			// fetch is left to be cancelled by parseCtx when this file parser returns
			// an error, so a dead volume is not held up waiting on its sibling probe.
			if requiresEncryptedRarVolume(meta, file.Filename, file.Subject) {
				p.log.Warn("required encrypted RAR volume is unavailable",
					"filename", file.Filename, "error", firstErr)
				return nil, NewNonRetryableError(
					fmt.Sprintf("required encrypted RAR volume %s is incomplete", file.Filename), firstErr)
			}
			if IsArticleUnavailable(firstErr) && !isRecovery {
				p.log.Warn("required content article is unavailable; rejecting NZB",
					"filename", file.Filename, "error", firstErr)
				return nil, NewNonRetryableError(
					fmt.Sprintf("required content article for %s is unavailable", file.Filename), firstErr)
			}
			// A transiently missing first article must not abort the import: the
			// filename comes from the (now-repaired) subject and the uniform body
			// size is recovered from another segment during normalization below.
			wg.Wait()
			p.log.Warn("first segment yEnc header unavailable; will recover sizing from other segments",
				"filename", file.Filename, "recovery", isRecovery, "error", firstErr)
		} else {
			wg.Wait()
			yencFilename = firstPartHeaders.FileName
			yencFileSize = int64(firstPartHeaders.FileSize)
			firstPartSize = int64(firstPartHeaders.PartSize)
		}
	}

	// Normalize segment sizes using yEnc PartSize headers if needed
	// This handles cases where NZB segment sizes include yEnc encoding overhead
	// This is required for all file types including RAR/7z since archive analysis
	// depends on accurate segment sizes for seeking within files
	if needLast {
		// Check for context cancellation before network call
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Reuse the already-fetched segment-0 PartSize and final-segment PartSize
		// (both resolved in parallel above); normalization recovers the uniform body
		// size from other segments when the first fetch was unavailable.
		err := p.normalizeSegmentSizesWithYenc(ctx, file.Segments, firstPartSize, lastPartSize, lastPartErr)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			// Normalization only errors when the uniform body part size could not be
			// resolved from ANY sampled segment — i.e. a genuinely damaged volume, not
			// a single transiently-missing article. Falling back to the overhead-laden
			// declared sizes would misalign every offset in this file, so abort it.
			p.log.Warn("could not normalize segment sizes; volume appears damaged",
				"error", err, "segments", len(file.Segments), "filename", file.Filename)
			return nil, NewNonRetryableError("failed to resolve yEnc segment sizes", err)
		}
	}

	// Convert segments
	segments := make([]*metapb.SegmentData, len(file.Segments))

	for i, seg := range file.Segments {
		segments[i] = &metapb.SegmentData{
			Id:          seg.ID,
			StartOffset: int64(0),
			EndOffset:   int64(seg.Bytes - 1),
			SegmentSize: int64(seg.Bytes),
		}
	}

	// Use yEnc file size if available, otherwise calculate using the sophisticated logic
	var totalSize int64
	if yencFileSize > 0 {
		totalSize = yencFileSize
	} else {
		var err error
		totalSize, err = p.calculateFileSize(file)
		if err != nil {
			// If we can't get the actual size, fallback to segment sum
			totalSize = p.calculateSegmentSum(file)
		}
	}

	var (
		password string
		salt     string
	)
	if meta != nil {
		if pwd, ok := meta["password"]; ok && pwd != "" {
			password = pwd
		}
		if s, ok := meta["salt"]; ok && s != "" {
			salt = s
		}
	}

	// Extract filename - priority: yEnc headers > meta file_name > file.Filename
	enc := metapb.Encryption_NONE // Default to no encryption

	// Start with yEnc filename if available, otherwise use NZB filename
	filename := yencFilename
	if filename == "" || IsProbablyObfuscated(filename) {
		filename = file.Filename
	}

	// Check metadata for overrides
	if meta != nil {
		if metaFilename, ok := meta["file_name"]; ok && metaFilename != "" {
			if _, ok := meta["file_size"]; ok {
				// This is a usenet-drive nzb with one file
				metaFilename = strings.TrimSuffix(nzbFilename, filepath.Ext(nzbFilename))
			}

			// This will add support for rclone encrypted files
			if strings.HasSuffix(strings.ToLower(metaFilename), rclone.EncFileExtension) {
				filename = metaFilename[:len(metaFilename)-4]
				enc = metapb.Encryption_RCLONE

				decSize, err := rclone.DecryptedSize(totalSize)
				if err != nil {
					return nil, NewNonRetryableError("failed to get decrypted size", err)
				}

				totalSize = decSize
			} else {
				filename = metaFilename
			}
		}

		if metaCipher, ok := meta["cipher"]; ok && metaCipher != "" {
			if metaCipher == string(encryption.RCloneCipherType) {
				enc = metapb.Encryption_RCLONE
			}
		}
	}

	// Attempt deobfuscation if filename appears obfuscated
	if IsProbablyObfuscated(filename) {
		p.log.Debug("Attempting deobfuscation", "filename", filename, "subject", file.Subject)

		// Attempt deobfuscation using all available files in the NZB
		if result := p.deobfuscator.DeobfuscateFilename(filename, allFiles, file); result.Success {
			filename = result.DeobfuscatedFilename
		} else {
			p.log.Warn("Unable to deobfuscate filename",
				"filename", filename,
				"subject", file.Subject)
		}
	}

	// Check if this is a RAR or 7z file
	isRarArchive := rarPattern.MatchString(filename)
	is7zArchive := sevenZipPattern.MatchString(filename)

	parsedFile := &ParsedFile{
		Subject:      file.Subject,
		Filename:     filename,
		Size:         totalSize,
		Segments:     segments,
		Groups:       file.Groups,
		IsRarArchive: isRarArchive,
		Is7zArchive:  is7zArchive,
		Encryption:   enc,
		Password:     password,
		Salt:         salt,
	}

	return parsedFile, nil
}

// calculateFileSize implements the sophisticated size calculation logic
func (p *Parser) calculateFileSize(file nzbparser.NzbFile) (int64, error) {
	// Priority 3: Different segment sizes - fetch yenc header to get actual file size
	if p.poolManager != nil && p.poolManager.HasPool() {
		if actualSize, err := p.fetchActualFileSizeFromYencHeader(file); err == nil {
			return actualSize, nil
		}
	}

	// Fallback: use segment sum if yenc header fetch failed
	return p.calculateSegmentSum(file), nil
}

// calculateSegmentSum calculates the total size by summing all segment sizes
func (p *Parser) calculateSegmentSum(file nzbparser.NzbFile) int64 {
	var segmentSum int64
	for _, seg := range file.Segments {
		segmentSum += int64(seg.Bytes)
	}
	return segmentSum
}

// fetchActualFileSizeFromYencHeader fetches the yenc header to get the actual file size
func (p *Parser) fetchActualFileSizeFromYencHeader(file nzbparser.NzbFile) (int64, error) {
	if p.poolManager == nil {
		return 0, NewNonRetryableError("no pool manager available", nil)
	}

	cp, err := p.poolManager.GetPool()
	if err != nil {
		return 0, NewNonRetryableError("no connection pool available", err)
	}

	if len(file.Segments) == 0 {
		return 0, fmt.Errorf("no segments available")
	}

	// Use first segment to get yenc headers
	firstSegment := file.Segments[0]

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	// Get a connection from the pool
	r, err := cp.BodyReader(ctx, firstSegment.ID, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to get body reader: %w", err)
	}
	r = guardNNTPBodyReader(r)
	if r == nil {
		return 0, fmt.Errorf("pool returned a nil body reader for segment %s", firstSegment.ID)
	}
	defer r.Close()

	// Get yenc headers
	h, err := getYencHeadersWithContext(ctx, r)
	if err != nil {
		return 0, fmt.Errorf("failed to get yenc headers: %w", err)
	}

	if h.FileSize <= 0 {
		return 0, fmt.Errorf("invalid file size from yenc header: %d", h.FileSize)
	}

	return int64(h.FileSize), nil
}

// fetchYencPartSize fetches the yenc header to get the actual part size for a specific segment
func (p *Parser) fetchYencHeaders(ctx context.Context, segment nzbparser.NzbSegment, groups []string) (nntpcli.YencHeaders, error) {
	if p.poolManager == nil {
		return nntpcli.YencHeaders{}, NewNonRetryableError("no pool manager available", nil)
	}

	cp, err := p.poolManager.GetPool()
	if err != nil {
		return nntpcli.YencHeaders{}, NewNonRetryableError("no connection pool available", err)
	}

	var result nntpcli.YencHeaders
	err = retry.Do(
		func() error {
			// Create context with timeout for each retry attempt
			ctx, cancel := context.WithTimeout(ctx, time.Second*30)
			defer cancel()

			// Get a connection from the pool
			r, err := cp.BodyReader(ctx, segment.ID, groups)
			if err != nil {
				// Check if the error indicates the article is missing from all providers
				// This is a permanent error that shouldn't be retried
				errMsg := err.Error()
				if strings.Contains(errMsg, "not found in any") || strings.Contains(errMsg, "no such article") {
					return NewNonRetryableError("article not available", fmt.Errorf("%w: %v", ErrArticleUnavailable, err))
				}
				return fmt.Errorf("failed to get body reader: %w", err)
			}
			r = guardNNTPBodyReader(r)
			if r == nil {
				return fmt.Errorf("pool returned a nil body reader for segment %s", segment.ID)
			}
			defer r.Close()

			// Get yenc headers. A sibling file parser may cancel this context
			// while BodyReader is returning; some nntppool versions can then
			// return a non-nil wrapper whose inner reader is nil, so the helper
			// below checks cancellation before calling into it.
			h, err := getYencHeadersWithContext(ctx, r)
			if err != nil {
				return fmt.Errorf("failed to get yenc headers: %w", err)
			}

			result = h
			return nil
		},
		retry.Attempts(3),
		retry.Context(ctx),
		retry.Delay(1*time.Second),
		retry.DelayType(retry.BackOffDelay),
		retry.MaxDelay(5*time.Second),
		retry.RetryIf(func(err error) bool {
			// Don't retry if this is a non-retryable error
			return !IsNonRetryable(err)
		}),
		retry.OnRetry(func(n uint, err error) {
			p.log.Warn("Retrying fetchYencHeaders",
				"attempt", n+1,
				"segment_id", segment.ID,
				"error", err)
		}),
	)
	if err != nil {
		return nntpcli.YencHeaders{}, err
	}

	if result.PartSize <= 0 {
		return nntpcli.YencHeaders{}, fmt.Errorf("invalid part size from yenc header: %d", result.PartSize)
	}

	return result, nil
}

// getYencHeadersWithContext contains failures from the external NNTP reader at
// the importer boundary. In particular, nntppool may return a typed wrapper
// around a nil reader when BodyReader races with context cancellation. Calling
// GetYencHeaders on that wrapper panics instead of returning an error.
func getYencHeadersWithContext(ctx context.Context, reader nntpcli.ArticleBodyReader) (headers nntpcli.YencHeaders, err error) {
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nntpcli.YencHeaders{}, ctxErr
		}
	}
	if reader == nil {
		return nntpcli.YencHeaders{}, errors.New("NNTP body reader is nil")
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			headers = nntpcli.YencHeaders{}
			err = fmt.Errorf("NNTP body reader panicked while reading yEnc headers: %v", recovered)
		}
	}()

	return reader.GetYencHeaders()
}

// normalizeSegmentSizesWithYenc normalizes segment sizes using yEnc PartSize headers
// This handles cases where NZB segment sizes include yEnc overhead
// normalizeSegmentSizesWithYenc rewrites NZB segment byte counts to the actual
// yEnc part sizes. firstPartSize is the segment-0 PartSize already obtained by the
// caller; lastPartSize/lastPartErr are the final segment's PartSize (and any error)
// fetched concurrently by the caller. When firstPartSize is <= 0 we skip re-fetching
// segment 0 and only recover the uniform body size from another leading segment.
func (p *Parser) normalizeSegmentSizesWithYenc(ctx context.Context, segments []nzbparser.NzbSegment, firstPartSize, lastPartSize int64, lastPartErr error) error {
	if len(segments) < 2 {
		// Not enough segments to determine if normalization is needed
		return nil
	}

	// Resolve the uniform body part size. Every non-last segment shares this size,
	// so if the caller's segment-0 fetch was missing we can recover it from any other
	// leading segment — a single transiently-absent article must not defeat sizing.
	if firstPartSize <= 0 {
		var err error
		firstPartSize, err = p.fetchUniformPartSize(ctx, segments)
		if firstPartSize <= 0 {
			return fmt.Errorf("could not resolve uniform yEnc part size from any leading segment: %w", err)
		}
	}

	// A confirmed all-provider miss on the last segment makes the content
	// incomplete and is terminal; transient header failures keep the declared size.
	if lastPartErr != nil {
		if IsArticleUnavailable(lastPartErr) {
			return lastPartErr
		}
		p.log.Warn("last segment yEnc header unavailable; keeping declared size for final segment",
			"error", lastPartErr, "segments", len(segments))
	}

	applyNormalizedSizes(segments, firstPartSize, lastPartSize)
	return nil
}

// lastSegmentPartSize fetches the yEnc PartSize of a file's final segment for
// normalization. It runs concurrently with the first-segment fetch so that both
// independent round-trips overlap, halving the per-file header-fetch wall-clock.
func (p *Parser) lastSegmentPartSize(ctx context.Context, segments []nzbparser.NzbSegment) (int64, error) {
	if len(segments) == 0 {
		return 0, nil
	}
	h, err := p.fetchYencHeaders(ctx, segments[len(segments)-1], nil)
	if err != nil {
		return 0, err
	}
	return int64(h.PartSize), nil
}

// fetchUniformPartSize resolves the yEnc part size shared by a file's uniform body
// segments, trying several leading segments so that a single transiently-missing
// article does not defeat size normalization. The last segment is never sampled
// because its size differs from the body. Returns 0 if none could be resolved.
func (p *Parser) fetchUniformPartSize(ctx context.Context, segments []nzbparser.NzbSegment) (int64, error) {
	maxTries := 5
	if limit := len(segments) - 1; maxTries > limit {
		maxTries = limit
	}
	var lastErr error
	for i := 0; i < maxTries; i++ {
		h, err := p.fetchYencHeaders(ctx, segments[i], nil)
		if err == nil && h.PartSize > 0 {
			return int64(h.PartSize), nil
		}
		if IsArticleUnavailable(err) {
			return 0, err
		}
		lastErr = err
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
	}
	return 0, lastErr
}

// applyNormalizedSizes overrides segment byte counts with the resolved yEnc part
// sizes: every segment except the last takes firstPartSize, the last takes
// lastPartSize. A non-positive size is left untouched so a missing header never
// zeroes out a segment.
func applyNormalizedSizes(segments []nzbparser.NzbSegment, firstPartSize, lastPartSize int64) {
	if len(segments) < 2 {
		return
	}
	if firstPartSize > 0 {
		for i := 0; i < len(segments)-1; i++ {
			segments[i].Bytes = int(firstPartSize)
		}
	}
	if lastPartSize > 0 {
		segments[len(segments)-1].Bytes = int(lastPartSize)
	}
}

// determineNzbType analyzes the parsed files to determine the NZB type
func (p *Parser) determineNzbType(files []ParsedFile) NzbType {
	if len(files) == 1 {
		// Single file NZB
		if files[0].IsRarArchive {
			return NzbTypeRarArchive
		}
		if files[0].Is7zArchive {
			return NzbType7zArchive
		}
		return NzbTypeSingleFile
	}

	// Multiple files - check if any are RAR or 7z archives
	hasRarFiles := false
	has7zFiles := false
	for _, file := range files {
		if file.IsRarArchive {
			hasRarFiles = true
		}
		if file.Is7zArchive {
			has7zFiles = true
		}
	}

	if hasRarFiles {
		return NzbTypeRarArchive
	}

	if has7zFiles {
		return NzbType7zArchive
	}

	return NzbTypeMultiFile
}

// GetMetadata extracts metadata from the NZB head section
func (p *Parser) GetMetadata(nzbXML *nzbparser.Nzb) map[string]string {
	metadata := make(map[string]string)

	if nzbXML.Meta == nil {
		return metadata
	}

	return nzbXML.Meta
}

// ValidateNzb performs basic validation on the parsed NZB
func (p *Parser) ValidateNzb(parsed *ParsedNzb) error {
	if parsed.TotalSize <= 0 {
		return NewNonRetryableError("invalid NZB: total size is zero", nil)
	}

	if parsed.SegmentsCount <= 0 {
		return NewNonRetryableError("invalid NZB: no segments found", nil)
	}

	for i, file := range parsed.Files {
		if len(file.Segments) == 0 {
			return NewNonRetryableError(fmt.Sprintf("invalid NZB: file %d has no segments", i), nil)
		}

		if file.Size <= 0 {
			return NewNonRetryableError(fmt.Sprintf("invalid NZB: file %d has invalid size", i), nil)
		}

		// Note: groups are optional — many indexers (Zyclops, NZBHydra) omit them.
		// Segment article IDs are globally unique, so groups aren't needed for downloading.
	}

	return nil
}
