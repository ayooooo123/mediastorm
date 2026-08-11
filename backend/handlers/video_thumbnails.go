package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/config"

	"github.com/gorilla/mux"
)

const (
	thumbnailDefaultIntervalSec = 60
	thumbnailMinIntervalSec     = 30
	thumbnailMaxCount           = 120
	thumbnailDefaultWorkers     = 1
	thumbnailMaxWorkers         = 8
	thumbnailWidth              = 640
	thumbnailFrameTimeout       = 45 * time.Second
	thumbnailFilterVersion      = 17
	thumbnailMinJPEGBytes       = 800
	thumbnailLibplaceboRuntime  = "libplacebo_runtime"
	thumbnailRateLimitRetries   = 2
	thumbnailRateLimitInitial   = 5 * time.Second
	thumbnailRateLimitMax       = 2 * time.Minute
	thumbnailDeferredFillDelay  = 10 * time.Second
)

type ThumbnailManager struct {
	baseDir      string
	ffmpegPath   string
	sourceBridge *thumbnailSourceBridge

	mu               sync.Mutex
	inFlight         map[string]struct{}
	filterOnce       sync.Once
	filterCaps       map[string]bool
	filterCapsErr    error
	protocolOnce     sync.Once
	sharedProtocol   bool
	protocolErr      error
	httpOptionsOnce  sync.Once
	httpInputOptions []string
	prewarmMu        sync.Mutex
	prewarmed        map[string]time.Time
}

type thumbnailToneMapMode string

const (
	thumbnailToneMapNone        thumbnailToneMapMode = "none"
	thumbnailToneMapLibplacebo  thumbnailToneMapMode = "libplacebo"
	thumbnailToneMapZscale      thumbnailToneMapMode = "zscale"
	thumbnailToneMapFFmpeg      thumbnailToneMapMode = "ffmpeg"
	thumbnailToneMapUnsupported thumbnailToneMapMode = "unsupported"
)

type thumbnailManifest struct {
	Key         string             `json:"key"`
	Status      string             `json:"status"`
	Phase       string             `json:"phase,omitempty"`
	PathHash    string             `json:"pathHash"`
	DurationSec float64            `json:"durationSec"`
	IntervalSec int                `json:"intervalSec"`
	Generated   int                `json:"generated"`
	Total       int                `json:"total"`
	UpdatedAt   time.Time          `json:"updatedAt"`
	Error       string             `json:"error,omitempty"`
	ToneMapped  bool               `json:"toneMapped,omitempty"`
	ToneMapMode string             `json:"toneMapMode,omitempty"`
	DVProfile   string             `json:"dvProfile,omitempty"`
	FilterVer   int                `json:"filterVersion,omitempty"`
	Thumbnails  []thumbnailDetails `json:"thumbnails"`
}

type thumbnailDetails struct {
	TimeSec float64 `json:"timeSec"`
	File    string  `json:"file"`
}

type thumbnailStatusResponse struct {
	Key         string             `json:"key"`
	Status      string             `json:"status"`
	Phase       string             `json:"phase,omitempty"`
	DurationSec float64            `json:"durationSec"`
	IntervalSec int                `json:"intervalSec"`
	Generated   int                `json:"generated"`
	Total       int                `json:"total"`
	UpdatedAt   time.Time          `json:"updatedAt"`
	Error       string             `json:"error,omitempty"`
	ToneMapped  bool               `json:"toneMapped,omitempty"`
	ToneMapMode string             `json:"toneMapMode,omitempty"`
	DVProfile   string             `json:"dvProfile,omitempty"`
	FilterVer   int                `json:"filterVersion,omitempty"`
	Thumbnails  []thumbnailURLItem `json:"thumbnails"`
}

type thumbnailURLItem struct {
	TimeSec float64 `json:"timeSec"`
	URL     string  `json:"url"`
}

type thumbnailJob struct {
	TimeSec    float64
	FileName   string
	OutputPath string
}

type thumbnailTarget struct {
	TimeSec  float64
	Priority bool
	Index    int
}

type thumbnailResult struct {
	Details thumbnailDetails
	OK      bool
}

type thumbnailRateLimitCooldown struct {
	mu        sync.Mutex
	nextDelay time.Duration
	until     time.Time
}

func NewThumbnailManager(baseDir, ffmpegPath string) *ThumbnailManager {
	return &ThumbnailManager{
		baseDir:      baseDir,
		ffmpegPath:   ffmpegPath,
		sourceBridge: newThumbnailSourceBridge(),
		inFlight:     make(map[string]struct{}),
		prewarmed:    make(map[string]time.Time),
	}
}

func thumbnailKey(cleanPath string) string {
	sum := sha256.Sum256([]byte(cleanPath))
	return hex.EncodeToString(sum[:])[:24]
}

func validThumbnailKey(key string) bool {
	if len(key) != 24 {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil
}

func cleanVideoPathParam(raw string) string {
	cleanPath := strings.TrimSpace(raw)
	if strings.HasPrefix(cleanPath, "/webdav/") {
		return strings.TrimPrefix(cleanPath, "/webdav")
	}
	if strings.HasPrefix(cleanPath, "webdav/") {
		return "/" + strings.TrimPrefix(cleanPath, "webdav/")
	}
	return cleanPath
}

func (m *ThumbnailManager) manifestPath(key string) string {
	return filepath.Join(m.baseDir, key, "manifest.json")
}

func (m *ThumbnailManager) readManifest(key string) (*thumbnailManifest, error) {
	data, err := os.ReadFile(m.manifestPath(key))
	if err != nil {
		return nil, err
	}
	var manifest thumbnailManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (m *ThumbnailManager) writeManifest(manifest *thumbnailManifest) error {
	dir := filepath.Join(m.baseDir, manifest.Key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	manifest.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "manifest.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "manifest.json"))
}

func thumbnailOutputUsable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() >= thumbnailMinJPEGBytes
}

func (m *ThumbnailManager) manifestFilesComplete(manifest *thumbnailManifest) bool {
	if manifest == nil || manifest.Generated == 0 || len(manifest.Thumbnails) == 0 {
		return false
	}
	dir := filepath.Join(m.baseDir, manifest.Key)
	usable := 0
	for _, thumb := range manifest.Thumbnails {
		if thumbnailOutputUsable(filepath.Join(dir, filepath.Base(thumb.File))) {
			usable++
		}
	}
	return usable == manifest.Generated
}

func thumbnailTimes(durationSec float64, requestedInterval int) (int, []float64) {
	if durationSec <= 0 || math.IsNaN(durationSec) || math.IsInf(durationSec, 0) {
		return thumbnailDefaultIntervalSec, nil
	}
	interval := requestedInterval
	if interval <= 0 {
		interval = thumbnailDefaultIntervalSec
	}
	if interval < thumbnailMinIntervalSec {
		interval = thumbnailMinIntervalSec
	}
	estimated := int(math.Ceil(durationSec / float64(interval)))
	if estimated > thumbnailMaxCount {
		interval = int(math.Ceil(durationSec / float64(thumbnailMaxCount)))
	}

	var times []float64
	for t := float64(interval) / 2; t < durationSec-1; t += float64(interval) {
		times = append(times, math.Round(t*10)/10)
	}
	if len(times) == 0 && durationSec > 5 {
		times = append(times, math.Round((durationSec/2)*10)/10)
	}
	if len(times) > thumbnailMaxCount {
		times = times[:thumbnailMaxCount]
	}
	return interval, times
}

func thumbnailGenerationTargets(durationSec float64, requestedInterval int, chapterTimes []float64) (int, []thumbnailTarget) {
	interval, regularTimes := thumbnailTimes(durationSec, requestedInterval)
	byTime := make(map[float64]bool, len(regularTimes)+len(chapterTimes))
	for _, chapterTime := range thumbnailChapterCaptureTimes(durationSec, chapterTimes) {
		byTime[chapterTime] = true
	}
	for _, regularTime := range regularTimes {
		if _, exists := byTime[regularTime]; !exists {
			byTime[regularTime] = false
		}
	}

	chronological := make([]thumbnailTarget, 0, len(byTime))
	for timestamp, priority := range byTime {
		chronological = append(chronological, thumbnailTarget{TimeSec: timestamp, Priority: priority})
	}
	sort.Slice(chronological, func(i, j int) bool { return chronological[i].TimeSec < chronological[j].TimeSec })
	for index := range chronological {
		chronological[index].Index = index
	}

	ordered := make([]thumbnailTarget, 0, min(len(chronological), thumbnailMaxCount))
	for _, target := range chronological {
		if target.Priority && len(ordered) < thumbnailMaxCount {
			ordered = append(ordered, target)
		}
	}
	for _, target := range chronological {
		if !target.Priority && len(ordered) < thumbnailMaxCount {
			ordered = append(ordered, target)
		}
	}
	return interval, ordered
}

func thumbnailChapterCaptureTimes(durationSec float64, chapterTimes []float64) []float64 {
	starts := make([]float64, 0, len(chapterTimes))
	for _, chapterTime := range chapterTimes {
		if isValidThumbnailTime(chapterTime, durationSec) {
			starts = append(starts, chapterTime)
		}
	}
	sort.Float64s(starts)

	captures := make([]float64, 0, len(starts))
	for index, start := range starts {
		if index > 0 && math.Abs(start-starts[index-1]) < 0.05 {
			continue
		}
		end := durationSec
		if index+1 < len(starts) && starts[index+1] > start {
			end = starts[index+1]
		}
		chapterDuration := end - start
		offset := math.Min(5, math.Max(1, chapterDuration*0.1))
		if offset >= chapterDuration {
			offset = chapterDuration / 2
		}
		captureTime := math.Round((start+offset)*10) / 10
		if isValidThumbnailTime(captureTime, durationSec) {
			captures = append(captures, captureTime)
		}
	}
	return captures
}

func isValidThumbnailTime(timestamp, durationSec float64) bool {
	return timestamp >= 0 && timestamp < durationSec-1 && !math.IsNaN(timestamp) && !math.IsInf(timestamp, 0)
}

func thumbnailNeedsToneMap(metadata *videoMetadataResponse) bool {
	if metadata == nil || len(metadata.VideoStreams) == 0 {
		return false
	}
	stream := metadata.VideoStreams[0]
	if stream.HasDolbyVision || strings.TrimSpace(stream.HdrFormat) != "" {
		return true
	}
	transfer := strings.ToLower(strings.TrimSpace(stream.ColorTransfer))
	return transfer == "smpte2084" || transfer == "arib-std-b67"
}

func isDolbyVisionProfile5(profile string) bool {
	normalized := strings.ToLower(strings.TrimSpace(profile))
	return strings.Contains(normalized, "dvhe.05") || strings.Contains(normalized, "dvh1.05") || strings.Contains(normalized, "profile 5") || normalized == "5"
}

func parseThumbnailDVProfile(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("dvProfile"))
}

func parseThumbnailToneMapHint(r *http.Request, _ string) bool {
	q := r.URL.Query()
	return parseBoolQuery(q.Get("toneMap")) || parseBoolQuery(q.Get("hdr")) || parseBoolQuery(q.Get("dv"))
}

func parseBoolQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func detectFFmpegVideoFilters(ctx context.Context, ffmpegPath string) (map[string]bool, error) {
	path := strings.TrimSpace(ffmpegPath)
	if path == "" {
		path = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, path, "-hide_banner", "-filters")
	output, err := cmd.CombinedOutput()
	caps := make(map[string]bool)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			caps[fields[1]] = true
		}
	}
	if err == nil && caps["libplacebo"] {
		if probeErr := probeFFmpegLibplacebo(ctx, path); probeErr == nil {
			caps[thumbnailLibplaceboRuntime] = true
		} else {
			log.Printf("[thumbnails] ffmpeg libplacebo filter present but runtime probe failed: %v", probeErr)
		}
	}
	return caps, err
}

func probeFFmpegLibplacebo(ctx context.Context, ffmpegPath string) error {
	cmd := exec.CommandContext(
		ctx,
		ffmpegPath,
		"-hide_banner",
		"-loglevel", "error",
		"-f", "lavfi",
		"-i", "color=c=black:s=16x16:d=0.1",
		"-frames:v", "1",
		"-vf", "libplacebo=w=16:h=16:colorspace=bt709:color_primaries=bt709:color_trc=bt709,format=yuv420p",
		"-f", "null",
		"-",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (m *ThumbnailManager) ffmpegFilterCaps() map[string]bool {
	m.filterOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		m.filterCaps, m.filterCapsErr = detectFFmpegVideoFilters(ctx, m.ffmpegPath)
		if m.filterCapsErr != nil {
			log.Printf("[thumbnails] unable to inspect ffmpeg filters: %v", m.filterCapsErr)
		}
	})
	if m.filterCaps == nil {
		return map[string]bool{}
	}
	return m.filterCaps
}

func (m *ThumbnailManager) thumbnailToneMapMode(toneMap bool, dvProfile string) thumbnailToneMapMode {
	if !toneMap {
		return thumbnailToneMapNone
	}
	caps := m.ffmpegFilterCaps()
	libplaceboUsable := caps["libplacebo"] && caps[thumbnailLibplaceboRuntime]
	if isDolbyVisionProfile5(dvProfile) {
		if libplaceboUsable {
			return thumbnailToneMapLibplacebo
		}
		return thumbnailToneMapUnsupported
	}
	if caps["zscale"] {
		return thumbnailToneMapZscale
	}
	if libplaceboUsable {
		return thumbnailToneMapLibplacebo
	}
	if caps["tonemap"] {
		return thumbnailToneMapFFmpeg
	}
	return thumbnailToneMapUnsupported
}

func thumbnailFilter(mode thumbnailToneMapMode) string {
	if mode == thumbnailToneMapNone {
		return fmt.Sprintf("scale=%d:-2:flags=lanczos,format=yuvj420p,setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=full", thumbnailWidth)
	}
	switch mode {
	case thumbnailToneMapLibplacebo:
		return fmt.Sprintf("libplacebo=w=%d:h=-2:tonemapping=bt.2390:colorspace=bt709:color_primaries=bt709:color_trc=bt709:range=tv,format=yuvj420p,setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=full", thumbnailWidth)
	case thumbnailToneMapZscale:
		return fmt.Sprintf("setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,zscale=t=linear:npl=100,format=gbrpf32le,tonemap=tonemap=mobius:param=0.35:desat=0:peak=1000,zscale=t=bt709:m=bt709:p=bt709:r=tv,eq=brightness=0.03:contrast=1.08:saturation=1.15:gamma=0.98,scale=%d:-2:flags=lanczos,format=yuvj420p,setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=full", thumbnailWidth)
	default:
		return fmt.Sprintf("format=gbrpf32le,tonemap=tonemap=mobius:param=0.35:desat=0:peak=1000,eq=brightness=0.18:contrast=1.38:saturation=2.15:gamma=0.85,scale=%d:-2:flags=lanczos,format=yuvj420p,setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=full", thumbnailWidth)
	}
}

func thumbnailInputUnavailable(output []byte) bool {
	text := strings.ToLower(string(output))
	return strings.Contains(text, "error opening input") ||
		strings.Contains(text, "connection refused") ||
		strings.Contains(text, "operation timed out") ||
		strings.Contains(text, "server returned 4xx") ||
		strings.Contains(text, "server returned 5xx")
}

func thumbnailRateLimited(output []byte) bool {
	text := strings.ToLower(string(output))
	return strings.Contains(text, "429") || strings.Contains(text, "too many requests") || strings.Contains(text, "rate limit")
}

func (c *thumbnailRateLimitCooldown) wait(key, cleanPath string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	waitFor := time.Until(c.until)
	c.mu.Unlock()
	if waitFor <= 0 {
		return
	}
	log.Printf("[thumbnails] rate-limit cooldown key=%s wait=%s path=%q", key, waitFor.Round(time.Second), cleanPath)
	timer := time.NewTimer(waitFor)
	defer timer.Stop()
	<-timer.C
}

func (c *thumbnailRateLimitCooldown) recordRateLimit() time.Duration {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delay := c.nextDelay
	if delay <= 0 {
		delay = thumbnailRateLimitInitial
	} else {
		delay *= 2
		if delay > thumbnailRateLimitMax {
			delay = thumbnailRateLimitMax
		}
	}
	c.nextDelay = delay
	c.until = time.Now().Add(delay)
	return delay
}

func (c *thumbnailRateLimitCooldown) recordSuccess() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.nextDelay = 0
	c.until = time.Time{}
	c.mu.Unlock()
}

func (m *ThumbnailManager) markUnsupported(cleanPath string, durationSec float64, requestedInterval int, dvProfile string, reason string) (string, error) {
	key := thumbnailKey(cleanPath)
	dir := filepath.Join(m.baseDir, key)
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("failed to clear unsupported thumbnail cache: %w", err)
	}
	interval, times := thumbnailTimes(durationSec, requestedInterval)
	manifest := &thumbnailManifest{
		Key:         key,
		Status:      "failed",
		PathHash:    key,
		DurationSec: durationSec,
		IntervalSec: interval,
		Generated:   0,
		Total:       len(times),
		Error:       reason,
		ToneMapped:  false,
		ToneMapMode: string(thumbnailToneMapUnsupported),
		DVProfile:   strings.TrimSpace(dvProfile),
		FilterVer:   thumbnailFilterVersion,
		Thumbnails:  []thumbnailDetails{},
	}
	if err := m.writeManifest(manifest); err != nil {
		return "", err
	}
	return key, nil
}

func (m *ThumbnailManager) start(cleanPath, sourceURL, authHeader string, durationSec float64, intervalSec int, workerCount int, toneMapMode thumbnailToneMapMode, dvProfile string, chapterTimes []float64) (string, bool, error) {
	if m == nil {
		return "", false, fmt.Errorf("thumbnail manager unavailable")
	}
	key := thumbnailKey(cleanPath)
	if manifest, err := m.readManifest(key); err == nil {
		manifestMode := thumbnailToneMapMode(strings.TrimSpace(manifest.ToneMapMode))
		if manifestMode == "" {
			if manifest.ToneMapped {
				manifestMode = thumbnailToneMapFFmpeg
			} else {
				manifestMode = thumbnailToneMapNone
			}
		}
		compatible := manifestMode == toneMapMode && strings.EqualFold(strings.TrimSpace(manifest.DVProfile), strings.TrimSpace(dvProfile)) && manifest.FilterVer == thumbnailFilterVersion
		if compatible && manifest.Status == "ready" && m.manifestFilesComplete(manifest) {
			return key, false, nil
		}
		if !compatible || manifest.Status == "failed" || manifest.Status == "ready" {
			if err := os.RemoveAll(filepath.Join(m.baseDir, key)); err != nil {
				return "", false, fmt.Errorf("failed to clear stale thumbnail cache for regeneration: %w", err)
			}
		}
	} else if err != nil && !os.IsNotExist(err) {
		if err := os.RemoveAll(filepath.Join(m.baseDir, key)); err != nil {
			return "", false, fmt.Errorf("failed to clear stale thumbnail cache for regeneration: %w", err)
		}
	}

	m.mu.Lock()
	if _, exists := m.inFlight[key]; exists {
		m.mu.Unlock()
		return key, false, nil
	}
	m.inFlight[key] = struct{}{}
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.inFlight, key)
			m.mu.Unlock()
		}()
		m.generate(key, cleanPath, sourceURL, authHeader, durationSec, intervalSec, workerCount, toneMapMode, dvProfile, chapterTimes)
	}()

	return key, true, nil
}

func (m *ThumbnailManager) generate(key, cleanPath, sourceURL, authHeader string, durationSec float64, requestedInterval int, workerCount int, toneMapMode thumbnailToneMapMode, dvProfile string, chapterTimes []float64) {
	interval, targets := thumbnailGenerationTargets(durationSec, requestedInterval, chapterTimes)
	workerCount = thumbnailWorkerCountFromSetting(workerCount)
	manifest := &thumbnailManifest{
		Key:         key,
		Status:      "generating",
		Phase:       "chapters",
		PathHash:    key,
		DurationSec: durationSec,
		IntervalSec: interval,
		Total:       len(targets),
		ToneMapped:  toneMapMode != thumbnailToneMapNone,
		ToneMapMode: string(toneMapMode),
		DVProfile:   strings.TrimSpace(dvProfile),
		FilterVer:   thumbnailFilterVersion,
		Thumbnails:  []thumbnailDetails{},
	}
	if err := m.writeManifest(manifest); err != nil {
		log.Printf("[thumbnails] failed to write initial manifest key=%s: %v", key, err)
		return
	}
	if len(targets) == 0 {
		manifest.Status = "failed"
		manifest.Error = "duration too short or unavailable"
		_ = m.writeManifest(manifest)
		return
	}
	if toneMapMode == thumbnailToneMapUnsupported {
		manifest.Status = "failed"
		manifest.Error = "thumbnail tone mapping unsupported by configured ffmpeg"
		_ = m.writeManifest(manifest)
		log.Printf("[thumbnails] unsupported tone-map mode key=%s path=%q dvProfile=%q", key, cleanPath, dvProfile)
		return
	}
	if toneMapMode != thumbnailToneMapNone {
		log.Printf("[thumbnails] tone-map enabled key=%s mode=%s path=%q", key, toneMapMode, cleanPath)
	}

	dir := filepath.Join(m.baseDir, key)
	cooldown := &thumbnailRateLimitCooldown{}
	priorityCount := 0
	for _, target := range targets {
		if target.Priority {
			priorityCount++
		}
	}
	log.Printf("[thumbnails] queue start key=%s chapterJobs=%d sequentialJobs=%d generated=%d/%d", key, priorityCount, len(targets)-priorityCount, manifest.Generated, manifest.Total)
	priorityTargets := targets[:priorityCount]
	deferredTargets := targets[priorityCount:]
	if len(priorityTargets) == 0 {
		manifest.Phase = "deferred"
		_ = m.writeManifest(manifest)
	}
	// Chapter cards are deliberately generated through one reader in timeline
	// order. This makes partial manifests deterministic even when the configured
	// scrubber worker count is higher.
	m.runThumbnailPhase(priorityTargets, 1, dir, key, cleanPath, sourceURL, authHeader, toneMapMode, dvProfile, cooldown, manifest)
	if len(priorityTargets) > 0 && len(deferredTargets) > 0 {
		manifest.Phase = "deferred"
		_ = m.writeManifest(manifest)
		log.Printf("[thumbnails] chapter phase complete key=%s generated=%d/%d; deferring scrubber fill by %s", key, manifest.Generated, manifest.Total, thumbnailDeferredFillDelay)
		timer := time.NewTimer(thumbnailDeferredFillDelay)
		<-timer.C
	}
	m.runThumbnailPhase(deferredTargets, workerCount, dir, key, cleanPath, sourceURL, authHeader, toneMapMode, dvProfile, cooldown, manifest)

	if manifest.Generated == 0 {
		manifest.Status = "failed"
		manifest.Error = "no thumbnails generated"
	} else {
		manifest.Status = "ready"
	}
	manifest.Phase = "complete"
	_ = m.writeManifest(manifest)
	log.Printf("[thumbnails] complete key=%s path=%q generated=%d/%d", key, cleanPath, manifest.Generated, manifest.Total)
}

func (m *ThumbnailManager) runThumbnailPhase(targets []thumbnailTarget, workerCount int, dir, key, cleanPath, sourceURL, authHeader string, toneMapMode thumbnailToneMapMode, dvProfile string, cooldown *thumbnailRateLimitCooldown, manifest *thumbnailManifest) {
	if len(targets) == 0 {
		return
	}
	workerCount = thumbnailWorkerCountFromSetting(workerCount)
	if len(targets) < workerCount {
		workerCount = len(targets)
	}
	results := make(chan thumbnailResult, workerCount)
	jobs := make(chan thumbnailJob)
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer wg.Done()
			for job := range jobs {
				results <- m.generateFrame(job, key, cleanPath, sourceURL, authHeader, toneMapMode, dvProfile, cooldown)
			}
		}()
	}
	go func() {
		for _, target := range targets {
			fileName := fmt.Sprintf("thumb-%04d.jpg", target.Index+1)
			jobs <- thumbnailJob{TimeSec: target.TimeSec, FileName: fileName, OutputPath: filepath.Join(dir, fileName)}
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	for result := range results {
		if !result.OK {
			continue
		}
		manifest.Thumbnails = append(manifest.Thumbnails, result.Details)
		sort.Slice(manifest.Thumbnails, func(i, j int) bool {
			return manifest.Thumbnails[i].TimeSec < manifest.Thumbnails[j].TimeSec
		})
		manifest.Generated = len(manifest.Thumbnails)
		_ = m.writeManifest(manifest)
	}
}

func thumbnailWorkerCountFromSetting(workers int) int {
	if workers < 1 {
		return thumbnailDefaultWorkers
	}
	if workers > thumbnailMaxWorkers {
		return thumbnailMaxWorkers
	}
	return workers
}

func (h *VideoHandler) thumbnailGenerationSettings() config.PlaybackThumbnailSettings {
	settings := config.PlaybackThumbnailSettings{Enabled: false, Workers: thumbnailDefaultWorkers}
	if h == nil || h.configManager == nil {
		return settings
	}
	loaded, err := h.configManager.Load()
	if err != nil {
		log.Printf("[thumbnails] failed to load thumbnail settings: %v", err)
		return settings
	}
	settings = loaded.Playback.Thumbnails
	settings.Workers = thumbnailWorkerCountFromSetting(settings.Workers)
	return settings
}

// PrewarmThumbnails resolves the final seekable source and establishes its
// reusable upstream connection while the client is still preparing playback.
// Frame generation remains chapter-driven once metadata reaches StartThumbnails.
func (h *VideoHandler) PrewarmThumbnails(path string) {
	if h == nil || h.thumbnailManager == nil || strings.TrimSpace(path) == "" || !h.thumbnailGenerationSettings().Enabled {
		return
	}
	cleanPath := cleanVideoPathParam(path)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), thumbnailSourcePrewarmTimeout)
		defer cancel()
		sourceURL, err := h.resolveSeekableURL(ctx, cleanPath)
		if err != nil || strings.TrimSpace(sourceURL) == "" {
			log.Printf("[thumbnails] unable to prewarm source key=%s path=%q: %v", thumbnailKey(cleanPath), cleanPath, err)
			return
		}
		h.thumbnailManager.prewarm(cleanPath, sourceURL, h.externalUsenetWebDAVAuthHeader(sourceURL))
	}()
}

func (m *ThumbnailManager) generateFrame(job thumbnailJob, key, cleanPath, sourceURL, authHeader string, toneMapMode thumbnailToneMapMode, dvProfile string, cooldown *thumbnailRateLimitCooldown) thumbnailResult {
	details := thumbnailDetails{TimeSec: job.TimeSec, File: job.FileName}
	if thumbnailOutputUsable(job.OutputPath) {
		return thumbnailResult{Details: details, OK: true}
	}
	_ = os.Remove(job.OutputPath)

	var output []byte
	var err error
	for attempt := 0; attempt <= thumbnailRateLimitRetries; attempt++ {
		cooldown.wait(key, cleanPath)
		output, err = m.runFrameCommand(job, sourceURL, authHeader, thumbnailFilter(toneMapMode))
		if err == nil || !thumbnailRateLimited(output) {
			break
		}
		delay := cooldown.recordRateLimit()
		log.Printf("[thumbnails] rate limited key=%s time=%.1f attempt=%d/%d cooldown=%s path=%q output=%s", key, job.TimeSec, attempt+1, thumbnailRateLimitRetries+1, delay, cleanPath, strings.TrimSpace(string(output)))
	}
	if err != nil && toneMapMode != thumbnailToneMapNone && !isDolbyVisionProfile5(dvProfile) && !thumbnailInputUnavailable(output) {
		log.Printf("[thumbnails] tone-map frame failed key=%s time=%.1f path=%q err=%v output=%s; retrying without tone map", key, job.TimeSec, cleanPath, err, strings.TrimSpace(string(output)))
		output, err = m.runFrameCommand(job, sourceURL, authHeader, thumbnailFilter(thumbnailToneMapNone))
	}
	if err != nil {
		if thumbnailInputUnavailable(output) {
			log.Printf("[thumbnails] source unavailable key=%s time=%.1f path=%q err=%v output=%s", key, job.TimeSec, cleanPath, err, strings.TrimSpace(string(output)))
		} else {
			log.Printf("[thumbnails] frame failed key=%s time=%.1f path=%q err=%v output=%s", key, job.TimeSec, cleanPath, err, strings.TrimSpace(string(output)))
		}
		return thumbnailResult{}
	}
	if !thumbnailOutputUsable(job.OutputPath) {
		size := int64(-1)
		if info, statErr := os.Stat(job.OutputPath); statErr == nil {
			size = info.Size()
		}
		log.Printf("[thumbnails] frame unusable key=%s time=%.1f path=%q file=%s size=%d", key, job.TimeSec, cleanPath, job.OutputPath, size)
		_ = os.Remove(job.OutputPath)
		return thumbnailResult{}
	}
	cooldown.recordSuccess()
	return thumbnailResult{Details: details, OK: true}
}

func (m *ThumbnailManager) runFrameCommand(job thumbnailJob, sourceURL, authHeader, filter string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), thumbnailFrameTimeout)
	defer cancel()
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-ss", fmt.Sprintf("%.2f", job.TimeSec),
	}
	sourceURL, authHeader, inputOptions := m.frameInput(thumbnailKeyForOutput(job.OutputPath), sourceURL, authHeader)
	args = append(args, inputOptions...)
	if authHeader != "" {
		args = append(args, "-headers", authHeader)
	}
	args = append(args,
		"-i", sourceURL,
		"-frames:v", "1",
		"-vf", filter,
		"-q:v", "5",
		job.OutputPath,
	)
	cmd := exec.CommandContext(ctx, m.ffmpegPath, args...)
	return cmd.CombinedOutput()
}

func thumbnailKeyForOutput(outputPath string) string {
	return filepath.Base(filepath.Dir(outputPath))
}

func (m *ThumbnailManager) isInflight(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.inFlight[key]
	return ok
}

func (h *VideoHandler) StartThumbnails(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		h.writeCommonHeaders(w)
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.WriteHeader(http.StatusOK)
		return
	}
	h.writeCommonHeaders(w)

	if h.thumbnailManager == nil {
		http.Error(w, "thumbnail generation unavailable", http.StatusServiceUnavailable)
		return
	}
	cleanPath := cleanVideoPathParam(r.URL.Query().Get("path"))
	if cleanPath == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}
	if !h.requireLibraryStreamAccess(w, r, cleanPath) {
		return
	}
	thumbnailSettings := h.thumbnailGenerationSettings()
	log.Printf("[thumbnails] start request key=%s enabled=%t workers=%d path=%q", thumbnailKey(cleanPath), thumbnailSettings.Enabled, thumbnailSettings.Workers, cleanPath)
	if !thumbnailSettings.Enabled {
		key := thumbnailKey(cleanPath)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"key":     key,
			"status":  "disabled",
			"started": false,
		})
		return
	}
	durationSec, _ := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("duration")), 64)
	intervalSec, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("interval")))
	chapterTimes := parseThumbnailChapterTimes(r.URL.Query()["chapter"])
	if len(chapterTimes) == 0 {
		if metadata := h.getCachedMetadata(cleanPath); metadata != nil {
			for _, chapter := range metadata.Chapters {
				chapterTimes = append(chapterTimes, chapter.Start)
			}
		}
	}
	dvProfile := parseThumbnailDVProfile(r)
	deferStart := parseBoolQuery(r.URL.Query().Get("defer"))
	if deferStart {
		key := thumbnailKey(cleanPath)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"key":     key,
			"status":  h.thumbnailStatusForKey(key),
			"started": false,
		})
		return
	}

	sourceURL, err := h.resolveSeekableURL(r.Context(), cleanPath)
	if err == nil && sourceURL != "" {
		log.Printf("[thumbnails] using seekable direct URL key=%s path=%q", thumbnailKey(cleanPath), cleanPath)
	} else {
		sourceURL = h.buildLocalVideoStreamURL(cleanPath)
		if sourceURL != "" {
			log.Printf("[thumbnails] using local stream proxy key=%s path=%q", thumbnailKey(cleanPath), cleanPath)
		} else {
			log.Printf("[thumbnails] no seekable URL for %q: %v", cleanPath, err)
			http.Error(w, "no seekable URL available", http.StatusNotImplemented)
			return
		}
	}

	toneMap := parseThumbnailToneMapHint(r, dvProfile) || thumbnailNeedsToneMap(h.getCachedMetadata(cleanPath))
	toneMapMode := h.thumbnailManager.thumbnailToneMapMode(toneMap, dvProfile)
	authHeader := h.externalUsenetWebDAVAuthHeader(sourceURL)
	key, started, err := h.thumbnailManager.start(cleanPath, sourceURL, authHeader, durationSec, intervalSec, thumbnailSettings.Workers, toneMapMode, dvProfile, chapterTimes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"key":     key,
		"status":  h.thumbnailStatusForKey(key),
		"started": started,
	})
}

func parseThumbnailChapterTimes(values []string) []float64 {
	times := make([]float64, 0, len(values))
	for _, value := range values {
		timestamp, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err == nil && timestamp >= 0 && !math.IsNaN(timestamp) && !math.IsInf(timestamp, 0) {
			times = append(times, timestamp)
		}
	}
	return times
}

func (h *VideoHandler) GetThumbnailsStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		h.writeCommonHeaders(w)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.WriteHeader(http.StatusOK)
		return
	}
	h.writeCommonHeaders(w)

	if h.thumbnailManager == nil {
		http.Error(w, "thumbnail generation unavailable", http.StatusServiceUnavailable)
		return
	}
	cleanPath := cleanVideoPathParam(r.URL.Query().Get("path"))
	if cleanPath == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}
	if !h.requireLibraryStreamAccess(w, r, cleanPath) {
		return
	}
	key := thumbnailKey(cleanPath)
	resp, err := h.thumbnailStatusResponse(r, key)
	if err != nil {
		resp = &thumbnailStatusResponse{
			Key:    key,
			Status: h.thumbnailStatusForKey(key),
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *VideoHandler) ServeThumbnailImage(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		h.writeCommonHeaders(w)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.WriteHeader(http.StatusOK)
		return
	}
	h.writeCommonHeaders(w)
	if h.thumbnailManager == nil {
		http.NotFound(w, r)
		return
	}
	vars := mux.Vars(r)
	key := vars["key"]
	file := filepath.Base(vars["file"])
	if !validThumbnailKey(key) || file == "." || !strings.HasSuffix(strings.ToLower(file), ".jpg") {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(h.thumbnailManager.baseDir, key, file)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, path)
}

func (h *VideoHandler) thumbnailStatusForKey(key string) string {
	if h.thumbnailManager == nil {
		return "disabled"
	}
	if manifest, err := h.thumbnailManager.readManifest(key); err == nil && manifest.Status != "" {
		if h.thumbnailManager.isInflight(key) && manifest.Status != "ready" {
			return "generating"
		}
		return manifest.Status
	}
	if h.thumbnailManager.isInflight(key) {
		return "generating"
	}
	if !h.thumbnailGenerationSettings().Enabled {
		return "disabled"
	}
	return "pending"
}

func (h *VideoHandler) thumbnailStatusResponse(r *http.Request, key string) (*thumbnailStatusResponse, error) {
	manifest, err := h.thumbnailManager.readManifest(key)
	if err != nil {
		return nil, err
	}
	status := manifest.Status
	if h.thumbnailManager.isInflight(key) && status != "ready" {
		status = "generating"
	}
	resp := &thumbnailStatusResponse{
		Key:         key,
		Status:      status,
		Phase:       manifest.Phase,
		DurationSec: manifest.DurationSec,
		IntervalSec: manifest.IntervalSec,
		Generated:   manifest.Generated,
		Total:       manifest.Total,
		UpdatedAt:   manifest.UpdatedAt,
		Error:       manifest.Error,
		ToneMapped:  manifest.ToneMapped,
		ToneMapMode: manifest.ToneMapMode,
		DVProfile:   manifest.DVProfile,
		FilterVer:   manifest.FilterVer,
		Thumbnails:  make([]thumbnailURLItem, 0, len(manifest.Thumbnails)),
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	dir := filepath.Join(h.thumbnailManager.baseDir, key)
	for _, thumb := range manifest.Thumbnails {
		file := filepath.Base(thumb.File)
		if !thumbnailOutputUsable(filepath.Join(dir, file)) {
			continue
		}
		u := fmt.Sprintf("/api/video/thumbnails/image/%s/%s", key, file)
		if token != "" {
			u += "?token=" + url.QueryEscape(token)
		}
		resp.Thumbnails = append(resp.Thumbnails, thumbnailURLItem{TimeSec: thumb.TimeSec, URL: u})
	}
	sort.Slice(resp.Thumbnails, func(i, j int) bool {
		return resp.Thumbnails[i].TimeSec < resp.Thumbnails[j].TimeSec
	})
	resp.Generated = len(resp.Thumbnails)
	return resp, nil
}
