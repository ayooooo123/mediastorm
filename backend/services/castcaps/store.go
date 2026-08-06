package castcaps

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// recordPrefix/recordSuffix bracket the per-receiver cache file name.
	recordPrefix = "receiver-"
	recordSuffix = ".json"
	// legacyCacheFile is the single-file cache the probing version of this
	// package wrote. Read once for its host and variant verdicts, never written.
	legacyCacheFile = "cast-capabilities.json"
)

// Store keeps what is known about each receiver across restarts.
//
// Everything it does is passive: it reads identity over HTTP and accepts
// observations from real playback. No method here can start playback.
type Store struct {
	dir string

	mu     sync.RWMutex
	byHost map[string]Capabilities

	// describeFn is the identity source, swapped in tests so the suite never
	// touches a receiver somebody is watching.
	describeFn func(ctx context.Context, host string) (Identity, error)
}

// NewStore opens (and creates on first write) a capability cache in dir.
func NewStore(dir string) *Store {
	store := &Store{
		dir:        dir,
		byHost:     map[string]Capabilities{},
		describeFn: Describe,
	}
	store.load()
	return store
}

// legacyRecord reads a cache entry written by the probing version of this
// package. Its identity fields had different names and its timestamp was
// probedAt; the embedded Capabilities picks up everything that did not move.
type legacyRecord struct {
	Capabilities
	Model        string    `json:"model"`
	BuildVersion string    `json:"buildVersion"`
	ProbedAt     time.Time `json:"probedAt"`
}

func (s *Store) load() {
	entries, err := os.ReadDir(s.dir)
	if err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasPrefix(name, recordPrefix) || !strings.HasSuffix(name, recordSuffix) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(s.dir, name))
			if err != nil {
				continue
			}
			var record Capabilities
			if err := json.Unmarshal(data, &record); err != nil {
				log.Printf("[castcaps] ignoring unreadable capability cache %s: %v", name, err)
				continue
			}
			s.adopt(record)
		}
	}
	s.loadLegacy()
	if len(s.byHost) > 0 {
		log.Printf("[castcaps] loaded capabilities for %d receiver(s)", len(s.byHost))
	}
}

// loadLegacy folds in the old single-file cache. Entries that also exist as a
// per-receiver file lose: that file is newer by construction.
func (s *Store) loadLegacy() {
	data, err := os.ReadFile(filepath.Join(s.dir, legacyCacheFile))
	if err != nil {
		return
	}
	var records []legacyRecord
	if err := json.Unmarshal(data, &records); err != nil {
		log.Printf("[castcaps] ignoring unreadable legacy capability cache: %v", err)
		return
	}
	for _, record := range records {
		caps := record.Capabilities
		if caps.ModelName == "" {
			caps.ModelName = record.Model
		}
		if caps.BuildRevision == "" {
			caps.BuildRevision = record.BuildVersion
		}
		if caps.UpdatedAt.IsZero() {
			caps.UpdatedAt = record.ProbedAt
		}
		if _, exists := s.byHost[strings.TrimSpace(caps.Host)]; exists {
			continue
		}
		s.adopt(caps)
	}
}

// adopt normalizes a decoded record into the cache. Verdict strings from older
// builds - "unknown", or anything a future build invents - degrade to
// VerdictUnknown rather than leaking through as a fourth state.
func (s *Store) adopt(record Capabilities) {
	host := strings.TrimSpace(record.Host)
	if host == "" {
		return
	}
	record.Host = host
	normalized := make(map[Variant]Verdict, len(record.Variants))
	for variant, verdict := range record.Variants {
		if strings.TrimSpace(string(variant)) == "" {
			continue
		}
		normalized[variant] = normalizeVerdict(verdict)
	}
	record.Variants = normalized
	s.byHost[host] = record
}

func (s *Store) persist(record Capabilities) {
	if s.dir == "" {
		return
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		log.Printf("[castcaps] failed to create cache dir: %v", err)
		return
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(s.dir, recordFileName(record.Host))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("[castcaps] failed to write capability cache: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("[castcaps] failed to replace capability cache: %v", err)
	}
}

// recordFileName keeps one file per receiver, named after its address with
// anything path-unsafe folded to an underscore.
func recordFileName(host string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.' || r == '-':
			return r
		default:
			return '_'
		}
	}, host)
	return recordPrefix + safe + recordSuffix
}

// Lookup returns cached capabilities for a receiver address, or nil. It never
// performs I/O, so the path that starts a cast can call it freely.
func (s *Store) Lookup(host string) *Capabilities {
	if s == nil {
		return nil
	}
	host = strings.TrimSpace(host)
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.byHost[host]
	if !ok {
		return nil
	}
	copied := record.clone()
	return &copied
}

// All returns every cached record.
func (s *Store) All() []Capabilities {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	records := make([]Capabilities, 0, len(s.byHost))
	for _, record := range s.byHost {
		records = append(records, record.clone())
	}
	return records
}

// Ensure reads a receiver's identity, folds in the prior it implies, and caches
// the result. Observed verdicts always survive: a prior can fill a gap but never
// argue with a measurement.
//
// A receiver that answers nothing is not written to the cache - there is no
// identity to attach - but a record already on file is returned instead, since a
// sleeping TV does not forget what it played yesterday.
func (s *Store) Ensure(ctx context.Context, host string) (*Capabilities, error) {
	if s == nil {
		return nil, errors.New("castcaps: nil store")
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, errors.New("castcaps: empty receiver host")
	}
	identity, err := s.describe(ctx, host)
	if err != nil {
		if cached := s.Lookup(host); cached != nil {
			return cached, nil
		}
		return nil, err
	}
	caps := s.applyIdentity(identity)
	return &caps, nil
}

func (s *Store) describe(ctx context.Context, host string) (Identity, error) {
	if s.describeFn == nil {
		return Describe(ctx, host)
	}
	return s.describeFn(ctx, host)
}

// applyIdentity refreshes identity and priors for one receiver and persists it.
func (s *Store) applyIdentity(identity Identity) Capabilities {
	s.mu.Lock()
	record, existed := s.byHost[identity.Host]
	if record.Variants == nil {
		record.Variants = map[Variant]Verdict{}
	}
	// A firmware update changes what a receiver accepts, which is the only
	// honest way out of a recorded rejection. Start the observations over.
	if existed && record.BuildRevision != "" && identity.BuildRevision != "" && record.BuildRevision != identity.BuildRevision {
		log.Printf("[castcaps] %s firmware changed %s -> %s: discarding observed verdicts",
			identity.Host, record.BuildRevision, identity.BuildRevision)
		record.Variants = map[Variant]Verdict{}
	}
	record.Identity = identity
	for variant, prior := range PriorFor(identity) {
		if observed(normalizeVerdict(record.Variants[variant])) {
			continue
		}
		record.Variants[variant] = prior
	}
	record.UpdatedAt = time.Now().UTC()
	s.byHost[identity.Host] = record
	copied := record.clone()
	s.mu.Unlock()

	s.persist(copied)
	return copied
}

// Record folds an observation from real playback into the cache.
//
// Precedence, in one sentence: a verdict only ever wins if it is more certain
// than what is already on file, ordered unknown < assumed < supported <
// rejected. So an observation overwrites a prior, a prior never overwrites an
// observation, and a rejection sticks even over an earlier success - a stall is
// a stall, and the user watches it happen. Ensure clears observations when the
// firmware revision changes, which is how a fixed receiver recovers.
func (s *Store) Record(host string, variant Variant, verdict Verdict) {
	if s == nil {
		return
	}
	host = strings.TrimSpace(host)
	if host == "" || strings.TrimSpace(string(variant)) == "" {
		return
	}
	verdict = normalizeVerdict(verdict)
	if verdict == VerdictUnknown {
		return // learned nothing
	}

	s.mu.Lock()
	record, existed := s.byHost[host]
	if !existed {
		record.Identity = Identity{Host: host, Name: host}
	}
	if record.Variants == nil {
		record.Variants = map[Variant]Verdict{}
	}
	current := normalizeVerdict(record.Variants[variant])
	if verdictRank(verdict) <= verdictRank(current) {
		s.mu.Unlock()
		return
	}
	record.Variants[variant] = verdict
	record.UpdatedAt = time.Now().UTC()
	s.byHost[host] = record
	copied := record.clone()
	s.mu.Unlock()

	log.Printf("[castcaps] %s: %s %s -> %s", host, variant, orUnknown(current), verdict)
	s.persist(copied)
}

func orUnknown(v Verdict) Verdict {
	if v == VerdictUnknown {
		return "unknown"
	}
	return v
}

// RefreshInBackground describes every receiver it can find on cidr and caches
// the priors their identities imply. Safe to run at any time, including mid-cast:
// it only issues HTTP GETs.
func (s *Store) RefreshInBackground(ctx context.Context, cidr string) {
	if s == nil {
		return
	}
	identities := Discover(ctx, cidr)
	log.Printf("[castcaps] discovery found %d Cast receiver(s) on %s", len(identities), cidr)
	for _, identity := range identities {
		s.applyIdentity(identity)
	}
}
