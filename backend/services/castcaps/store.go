package castcaps

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// recheckAfter re-probes a receiver whose answers have gone stale. Firmware
// updates change what a receiver accepts, and the build version check below
// catches most of those immediately; this is the backstop.
const recheckAfter = 30 * 24 * time.Hour

func insecureTLSConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, // #nosec G402 - device-local self-signed certs
		MaxVersion:         tls.VersionTLS12,
	}
}

// Store keeps probe results across restarts and makes sure a receiver is only
// probed once at a time.
type Store struct {
	path string

	mu       sync.RWMutex
	byID     map[string]Capabilities
	inFlight map[string]bool

	// URLForVariant builds the probe stream URL the receiver should fetch.
	URLForVariant func(Variant) string
}

func NewStore(cacheDir string) *Store {
	store := &Store{
		path:     filepath.Join(cacheDir, "cast-capabilities.json"),
		byID:     map[string]Capabilities{},
		inFlight: map[string]bool{},
	}
	store.load()
	return store
}

func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var records []Capabilities
	if err := json.Unmarshal(data, &records); err != nil {
		log.Printf("[castcaps] ignoring unreadable capability cache: %v", err)
		return
	}
	for _, record := range records {
		s.byID[record.ReceiverID] = record
	}
	log.Printf("[castcaps] loaded capabilities for %d receiver(s)", len(records))
}

func (s *Store) save() {
	s.mu.RLock()
	records := make([]Capabilities, 0, len(s.byID))
	for _, record := range s.byID {
		records = append(records, record)
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("[castcaps] failed to write capability cache: %v", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		log.Printf("[castcaps] failed to replace capability cache: %v", err)
	}
}

// Lookup returns cached capabilities for a receiver address. Callers on the
// cast start path use this and nothing else: it never blocks on a probe.
func (s *Store) Lookup(host string) *Capabilities {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, record := range s.byID {
		if record.Host == host {
			copied := record
			return &copied
		}
	}
	return nil
}

// All returns every cached record.
func (s *Store) All() []Capabilities {
	s.mu.RLock()
	defer s.mu.RUnlock()
	records := make([]Capabilities, 0, len(s.byID))
	for _, record := range s.byID {
		records = append(records, record)
	}
	return records
}

func (s *Store) put(caps Capabilities) {
	s.mu.Lock()
	s.byID[caps.ReceiverID] = caps
	s.mu.Unlock()
	s.save()
}

// NeedsProbe reports whether a device has no usable answer on file.
func (s *Store) NeedsProbe(device Device) bool {
	s.mu.RLock()
	record, ok := s.byID[device.ID()]
	s.mu.RUnlock()
	if !ok || record.Partial {
		return true
	}
	if device.BuildVersion != "" && record.BuildVersion != device.BuildVersion {
		return true
	}
	return time.Since(record.ProbedAt) > recheckAfter
}

// Probe runs the variant matrix against a device and caches the result. It
// refuses to run twice concurrently for the same receiver.
func (s *Store) Probe(ctx context.Context, device Device) (Capabilities, error) {
	if s.URLForVariant == nil {
		return Capabilities{}, fmt.Errorf("probe URL builder is not configured")
	}
	id := device.ID()

	s.mu.Lock()
	if s.inFlight[id] {
		s.mu.Unlock()
		return Capabilities{}, fmt.Errorf("probe already running for %s", device.Host)
	}
	s.inFlight[id] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inFlight, id)
		s.mu.Unlock()
	}()

	log.Printf("[castcaps] probing receiver %q (%s, %s)", device.Name, device.Host, device.Model)
	caps := ProbeReceiver(ctx, device, s.URLForVariant)
	s.put(caps)
	return caps, nil
}

// RefreshInBackground discovers receivers and probes the ones with no usable
// answer. Deliberately not called from any cast start path: probing takes over
// a receiver for a few seconds, so it only runs on demand or on a schedule
// while nothing is being cast.
func (s *Store) RefreshInBackground(ctx context.Context, cidr string) {
	devices := Discover(ctx, cidr)
	log.Printf("[castcaps] discovery found %d Cast receiver(s) on %s", len(devices), cidr)
	for _, device := range devices {
		if !s.NeedsProbe(device) {
			continue
		}
		if _, err := s.Probe(ctx, device); err != nil {
			log.Printf("[castcaps] probe of %s failed: %v", device.Host, err)
		}
	}
}
