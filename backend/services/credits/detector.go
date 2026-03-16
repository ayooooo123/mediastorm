package credits

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// DetectionResult holds the result of credits detection for a video.
type DetectionResult struct {
	Detected        bool    `json:"detected"`
	CreditsStartSec float64 `json:"credits_start_sec,omitempty"`
}

// Detector manages credits detection by invoking a Python script.
type Detector struct {
	mu       sync.RWMutex
	cache    map[string]*DetectionResult
	inflight sync.Map // dedup concurrent requests; key=streamPath, value=struct{}
	sem      chan struct{}
}

// NewDetector creates a new credits detector with a concurrency limit of 2.
func NewDetector() *Detector {
	return &Detector{
		cache: make(map[string]*DetectionResult),
		sem:   make(chan struct{}, 2),
	}
}

// Get returns a cached detection result, or nil if not yet available.
func (d *Detector) Get(streamPath string) *DetectionResult {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.cache[streamPath]
}

// IsInflight returns true if detection is currently running for this path.
func (d *Detector) IsInflight(streamPath string) bool {
	_, ok := d.inflight.Load(streamPath)
	return ok
}

// DetectAsync launches credits detection in a background goroutine.
// It deduplicates concurrent requests for the same path.
func (d *Detector) DetectAsync(ctx context.Context, streamPath, directURL string, duration float64) {
	// Already cached?
	if d.Get(streamPath) != nil {
		return
	}

	// Already in-flight?
	if _, loaded := d.inflight.LoadOrStore(streamPath, struct{}{}); loaded {
		return
	}

	go func() {
		defer d.inflight.Delete(streamPath)

		// Acquire semaphore (limit concurrency)
		d.sem <- struct{}{}
		defer func() { <-d.sem }()

		log.Printf("[credits] starting detection for path=%q duration=%.0fs", streamPath, duration)

		// Use a 3-minute timeout for the detection process
		detectCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		result, err := d.runPython(detectCtx, directURL, duration)
		if err != nil {
			log.Printf("[credits] detection failed for path=%q: %v", streamPath, err)
			// Don't cache script errors — allow retry on next playback
			return
		}

		d.mu.Lock()
		d.cache[streamPath] = result
		d.mu.Unlock()

		if result.Detected {
			log.Printf("[credits] detected credits at %.1fs for path=%q", result.CreditsStartSec, streamPath)
		} else {
			log.Printf("[credits] no credits detected for path=%q", streamPath)
		}
	}()
}

// runPython invokes the detect_credits.py script and parses its JSON output.
func (d *Detector) runPython(ctx context.Context, videoURL string, duration float64) (*DetectionResult, error) {
	scriptPath, pythonPath, err := getScriptPaths()
	if err != nil {
		return nil, fmt.Errorf("locating script: %w", err)
	}

	cmd := exec.CommandContext(ctx, pythonPath, scriptPath,
		"--url", videoURL,
		"--duration", fmt.Sprintf("%.1f", duration),
	)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("script exited %d: %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return nil, err
	}

	var result DetectionResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("parsing output: %w (raw: %s)", err, string(output))
	}

	return &result, nil
}

// getScriptPaths returns paths to the Python interpreter and credits detection script.
func getScriptPaths() (scriptPath, pythonPath string, err error) {
	// Docker paths
	dockerScript := "/detect_credits.py"
	dockerPython := "/.venv/bin/python3"

	if _, err := os.Stat(dockerScript); err == nil {
		if _, err := os.Stat(dockerPython); err == nil {
			return dockerScript, dockerPython, nil
		}
	}

	// Local development paths
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", "", fmt.Errorf("failed to get current file path")
	}

	// From backend/services/credits/, go up 2 levels to backend/
	scriptPath = filepath.Join(filepath.Dir(currentFile), "..", "..", "detect_credits.py")
	// From backend/services/credits/, go up 3 levels to project root for .venv
	pythonPath = filepath.Join(filepath.Dir(currentFile), "..", "..", "..", ".venv", "bin", "python3")

	return scriptPath, pythonPath, nil
}
