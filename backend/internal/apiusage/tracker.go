package apiusage

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	outboundRetention       = 7 * 24 * time.Hour
	outboundWindowRetention = 24 * time.Hour
)

type EndpointUsage struct {
	Key             string    `json:"key"`
	Label           string    `json:"label"`
	Group           string    `json:"group"`
	Method          string    `json:"method"`
	Path            string    `json:"path"`
	Count           int64     `json:"count"`
	SuccessCount    int64     `json:"successCount"`
	FailureCount    int64     `json:"failureCount"`
	LastStatus      int       `json:"lastStatus"`
	LastDurationMS  int64     `json:"lastDurationMs"`
	TotalDurationMS int64     `json:"totalDurationMs"`
	LastCalledAt    time.Time `json:"lastCalledAt"`
}

type OutboundUsage struct {
	Key             string    `json:"key"`
	Provider        string    `json:"provider"`
	Operation       string    `json:"operation"`
	Method          string    `json:"method"`
	Host            string    `json:"host"`
	LastPath        string    `json:"lastPath"`
	Count           int64     `json:"count"`
	LastHourCount   int64     `json:"lastHourCount"`
	Last24HourCount int64     `json:"last24HourCount"`
	SuccessCount    int64     `json:"successCount"`
	FailureCount    int64     `json:"failureCount"`
	LastStatus      int       `json:"lastStatus"`
	LastDurationMS  int64     `json:"lastDurationMs"`
	TotalDurationMS int64     `json:"totalDurationMs"`
	LastCalledAt    time.Time `json:"lastCalledAt"`
}

type outboundEvent struct {
	Key        string    `json:"key"`
	Provider   string    `json:"provider"`
	Operation  string    `json:"operation"`
	Method     string    `json:"method"`
	Host       string    `json:"host"`
	Path       string    `json:"path"`
	Status     int       `json:"status"`
	DurationMS int64     `json:"durationMs"`
	At         time.Time `json:"at"`
}

type Tracker struct {
	mu             sync.RWMutex
	endpoints      map[string]EndpointUsage
	outbound       map[string]OutboundUsage
	outboundEvents []outboundEvent
	storageDir     string
	storageMu      sync.Mutex
}

type trackedRoundTripper struct {
	base      http.RoundTripper
	provider  string
	operation string
}

var globalTracker = &Tracker{
	endpoints: make(map[string]EndpointUsage),
	outbound:  make(map[string]OutboundUsage),
}

func GetTracker() *Tracker {
	return globalTracker
}

func ConfigureStorage(cacheDir string) {
	GetTracker().ConfigureStorage(cacheDir)
}

func TrackClient(client *http.Client, provider, operation string) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	clone := *client
	base := clone.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	if _, ok := base.(*trackedRoundTripper); !ok {
		clone.Transport = &trackedRoundTripper{
			base:      base,
			provider:  provider,
			operation: operation,
		}
	}
	return &clone
}

func (t *trackedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	startedAt := time.Now()
	resp, err := base.RoundTrip(req)
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	rawURL := ""
	method := ""
	if req != nil {
		method = req.Method
		if req.URL != nil {
			rawURL = req.URL.String()
		}
	}
	GetTracker().RecordOutbound(t.provider, t.operation, method, rawURL, status, time.Since(startedAt))
	return resp, err
}

func (t *Tracker) ConfigureStorage(cacheDir string) {
	cacheDir = strings.TrimSpace(cacheDir)
	if cacheDir == "" {
		cacheDir = "cache"
	}
	storageDir := filepath.Join(cacheDir, "api-usage")
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		log.Printf("[apiusage] warning: failed to create usage cache dir %s: %v", storageDir, err)
		return
	}

	t.mu.Lock()
	t.storageDir = storageDir
	t.mu.Unlock()

	if err := t.loadOutboundEvents(time.Now()); err != nil {
		log.Printf("[apiusage] warning: failed to load usage cache: %v", err)
	}
	if err := t.pruneOutboundFiles(time.Now()); err != nil {
		log.Printf("[apiusage] warning: failed to prune usage cache: %v", err)
	}
}

func (t *Tracker) Record(key, label, group, method, path string, status int, duration time.Duration) {
	if key == "" {
		return
	}
	if status == 0 {
		status = http.StatusOK
	}
	durationMS := duration.Milliseconds()
	if durationMS < 0 {
		durationMS = 0
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.endpoints == nil {
		t.endpoints = make(map[string]EndpointUsage)
	}

	entry := t.endpoints[key]
	entry.Key = key
	entry.Label = label
	entry.Group = group
	entry.Method = method
	entry.Path = path
	entry.Count++
	if status >= 400 {
		entry.FailureCount++
	} else {
		entry.SuccessCount++
	}
	entry.LastStatus = status
	entry.LastDurationMS = durationMS
	entry.TotalDurationMS += durationMS
	entry.LastCalledAt = time.Now()
	t.endpoints[key] = entry
}

func (t *Tracker) RecordOutbound(provider, operation, method, rawURL string, status int, duration time.Duration) {
	t.recordOutboundAt(time.Now(), provider, operation, method, rawURL, status, duration, true)
}

func (t *Tracker) recordOutboundAt(now time.Time, provider, operation, method, rawURL string, status int, duration time.Duration, persist bool) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = "unknown"
	}
	operation = strings.TrimSpace(operation)
	if operation == "" {
		operation = "request"
	}
	method = strings.TrimSpace(method)
	if method == "" {
		method = http.MethodGet
	}

	host := ""
	path := ""
	if parsed, err := url.Parse(rawURL); err == nil {
		host = strings.ToLower(parsed.Hostname())
		path = sanitizePath(parsed.EscapedPath())
	}
	if host == "" {
		host = "unknown"
	}
	if path == "" {
		path = "/"
	}

	durationMS := duration.Milliseconds()
	if durationMS < 0 {
		durationMS = 0
	}
	key := strings.ToLower(provider) + "|" + strings.ToLower(operation) + "|" + strings.ToUpper(method) + "|" + host

	event := outboundEvent{
		Key:        key,
		Provider:   provider,
		Operation:  operation,
		Method:     strings.ToUpper(method),
		Host:       host,
		Path:       path,
		Status:     status,
		DurationMS: durationMS,
		At:         now,
	}

	t.mu.Lock()
	if t.outbound == nil {
		t.outbound = make(map[string]OutboundUsage)
	}

	entry := t.outbound[key]
	entry.Key = key
	entry.Provider = provider
	entry.Operation = operation
	entry.Method = strings.ToUpper(method)
	entry.Host = host
	entry.LastPath = path
	entry.Count++
	if status > 0 && status < 400 {
		entry.SuccessCount++
	} else {
		entry.FailureCount++
	}
	entry.LastStatus = status
	entry.LastDurationMS = durationMS
	entry.TotalDurationMS += durationMS
	entry.LastCalledAt = now
	t.outbound[key] = entry

	t.outboundEvents = append(t.outboundEvents, event)
	cutoff := now.Add(-24 * time.Hour)
	keepFrom := 0
	for keepFrom < len(t.outboundEvents) && t.outboundEvents[keepFrom].At.Before(cutoff) {
		keepFrom++
	}
	if keepFrom > 0 {
		t.outboundEvents = append([]outboundEvent(nil), t.outboundEvents[keepFrom:]...)
	}
	storageDir := t.storageDir
	t.mu.Unlock()

	if persist && storageDir != "" {
		t.storageMu.Lock()
		defer t.storageMu.Unlock()
		if err := appendOutboundEvent(storageDir, event); err != nil {
			log.Printf("[apiusage] warning: failed to persist usage event: %v", err)
		}
	}
}

func sanitizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "/" {
		return path
	}
	leadingSlash := strings.HasPrefix(path, "/")
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "" {
			continue
		}
		lower := strings.ToLower(part)
		if len(part) > 64 ||
			strings.Contains(lower, "apikey") ||
			strings.Contains(lower, "api_key") ||
			strings.Contains(lower, "token") ||
			strings.Contains(lower, "password") ||
			strings.Contains(lower, "secret") ||
			strings.Contains(lower, "authorization") ||
			strings.Contains(lower, "bearer") {
			parts[i] = "[redacted]"
		}
	}
	sanitized := strings.Join(parts, "/")
	if leadingSlash && !strings.HasPrefix(sanitized, "/") {
		sanitized = "/" + sanitized
	}
	return sanitized
}

func (t *Tracker) Snapshot() []EndpointUsage {
	t.mu.RLock()
	defer t.mu.RUnlock()

	entries := make([]EndpointUsage, 0, len(t.endpoints))
	for _, entry := range t.endpoints {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Group != entries[j].Group {
			return entries[i].Group < entries[j].Group
		}
		return entries[i].Label < entries[j].Label
	})
	return entries
}

func (t *Tracker) SnapshotOutbound() []OutboundUsage {
	return t.snapshotOutboundAt(time.Now())
}

func (t *Tracker) snapshotOutboundAt(now time.Time) []OutboundUsage {
	t.mu.RLock()
	defer t.mu.RUnlock()

	hourCutoff := now.Add(-1 * time.Hour)
	dayCutoff := now.Add(-24 * time.Hour)
	hourCounts := make(map[string]int64)
	dayCounts := make(map[string]int64)
	for _, event := range t.outboundEvents {
		if !event.At.Before(dayCutoff) {
			dayCounts[event.Key]++
		}
		if !event.At.Before(hourCutoff) {
			hourCounts[event.Key]++
		}
	}

	entries := make([]OutboundUsage, 0, len(t.outbound))
	for key, entry := range t.outbound {
		entry.LastHourCount = hourCounts[key]
		entry.Last24HourCount = dayCounts[key]
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Last24HourCount != entries[j].Last24HourCount {
			return entries[i].Last24HourCount > entries[j].Last24HourCount
		}
		if entries[i].Provider != entries[j].Provider {
			return entries[i].Provider < entries[j].Provider
		}
		return entries[i].Operation < entries[j].Operation
	})
	return entries
}

func (t *Tracker) loadOutboundEvents(now time.Time) error {
	t.mu.RLock()
	storageDir := t.storageDir
	t.mu.RUnlock()
	if storageDir == "" {
		return nil
	}

	entries, err := os.ReadDir(storageDir)
	if err != nil {
		return err
	}
	cutoff := now.Add(-outboundRetention)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "outbound-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(storageDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var event outboundEvent
			if err := json.Unmarshal([]byte(line), &event); err != nil || event.At.Before(cutoff) {
				continue
			}
			t.recordLoadedOutboundEvent(event)
		}
	}
	return nil
}

func (t *Tracker) recordLoadedOutboundEvent(event outboundEvent) {
	duration := time.Duration(event.DurationMS) * time.Millisecond
	t.recordOutboundAt(event.At, event.Provider, event.Operation, event.Method, "https://"+event.Host+event.Path, event.Status, duration, false)
}

func appendOutboundEvent(storageDir string, event outboundEvent) error {
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(storageDir, "outbound-"+event.At.Format("2006-01-02")+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func (t *Tracker) pruneOutboundFiles(now time.Time) error {
	t.mu.RLock()
	storageDir := t.storageDir
	t.mu.RUnlock()
	if storageDir == "" {
		return nil
	}

	entries, err := os.ReadDir(storageDir)
	if err != nil {
		return err
	}
	cutoffDay := now.Add(-outboundRetention).Format("2006-01-02")
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "outbound-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, "outbound-"), ".jsonl")
		if day < cutoffDay {
			_ = os.Remove(filepath.Join(storageDir, name))
		}
	}
	return nil
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func Do(client *http.Client, provider, operation string, req *http.Request) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	startedAt := time.Now()
	resp, err := client.Do(req)
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	rawURL := ""
	method := ""
	if req != nil {
		method = req.Method
		if req.URL != nil {
			rawURL = req.URL.String()
		}
	}
	GetTracker().RecordOutbound(provider, operation, method, rawURL, status, time.Since(startedAt))
	return resp, err
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func Track(key, label, group string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		defer func() {
			GetTracker().Record(key, label, group, r.Method, r.URL.Path, recorder.status, time.Since(startedAt))
		}()
		next.ServeHTTP(recorder, r)
	}
}
