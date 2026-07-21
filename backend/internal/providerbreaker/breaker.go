package providerbreaker

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultCooldown = time.Hour
	maximumCooldown = 24 * time.Hour
)

type state struct {
	until         time.Time
	failures      int
	probeInFlight bool
}

// Breaker prevents a rate-limited indexer from being retried for every query
// variant and every ranked result. A single request is allowed after the
// cooldown as a half-open probe.
type Breaker struct {
	mu     sync.Mutex
	states map[string]state
	now    func() time.Time
}

var shared = New()

func New() *Breaker {
	return &Breaker{
		states: make(map[string]state),
		now:    time.Now,
	}
}

// Shared returns the process-wide breaker used by indexer searches and NZB
// downloads so a 429 observed by either path protects both paths.
func Shared() *Breaker {
	return shared
}

func normalize(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

// Allow reports whether a provider request may proceed. probe is true when
// this is the sole half-open request allowed after an existing cooldown.
func (b *Breaker) Allow(provider string) (allowed bool, until time.Time, probe bool) {
	key := normalize(provider)
	if key == "" {
		return true, time.Time{}, false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	current, ok := b.states[key]
	if !ok {
		return true, time.Time{}, false
	}
	now := b.now()
	if now.Before(current.until) {
		return false, current.until, false
	}
	if current.probeInFlight {
		return false, current.until, false
	}
	current.probeInFlight = true
	b.states[key] = current
	return true, current.until, true
}

// RecordRateLimit opens or extends the provider circuit. Retry hints are
// honored, but never shorten the exponential cooldown used when an indexer
// omits a reliable quota reset time.
func (b *Breaker) RecordRateLimit(provider string, retryHint time.Duration) time.Time {
	key := normalize(provider)
	if key == "" {
		return time.Time{}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	current := b.states[key]
	current.failures++
	delay := defaultCooldown
	for i := 1; i < current.failures && delay < maximumCooldown; i++ {
		delay *= 2
		if delay > maximumCooldown {
			delay = maximumCooldown
		}
	}
	if retryHint > delay {
		delay = retryHint
	}
	if delay > maximumCooldown {
		delay = maximumCooldown
	}
	current.until = b.now().Add(delay)
	current.probeInFlight = false
	b.states[key] = current
	return current.until
}

// RecordSuccess closes a half-open circuit after its probe succeeds. Ordinary
// concurrent successes do not erase a 429 recorded by another request.
func (b *Breaker) RecordSuccess(provider string, probe bool) {
	if !probe {
		return
	}
	key := normalize(provider)
	if key == "" {
		return
	}
	b.mu.Lock()
	delete(b.states, key)
	b.mu.Unlock()
}

// ReleaseProbe allows another half-open probe after a non-rate-limit failure.
func (b *Breaker) ReleaseProbe(provider string, probe bool) {
	if !probe {
		return
	}
	key := normalize(provider)
	b.mu.Lock()
	if current, ok := b.states[key]; ok {
		current.probeInFlight = false
		b.states[key] = current
	}
	b.mu.Unlock()
}

// RetryHint extracts a Retry-After header. Bodies are deliberately not parsed:
// indexer HTML messages often describe a short anti-hammer delay even when the
// account has exhausted a longer rolling quota.
func RetryHint(header http.Header, now time.Time) time.Duration {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(value); err == nil && retryAt.After(now) {
		return retryAt.Sub(now)
	}
	return 0
}
