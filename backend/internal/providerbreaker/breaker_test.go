package providerbreaker

import (
	"net/http"
	"testing"
	"time"
)

func TestBreakerRateLimitAndHalfOpenProbe(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	breaker := New()
	breaker.now = func() time.Time { return now }

	if allowed, _, probe := breaker.Allow("Ninja"); !allowed || probe {
		t.Fatalf("initial Allow = (%v, %v), want allowed non-probe", allowed, probe)
	}

	until := breaker.RecordRateLimit("Ninja", 5*time.Minute)
	if want := now.Add(time.Hour); !until.Equal(want) {
		t.Fatalf("first cooldown until = %v, want %v", until, want)
	}
	if allowed, gotUntil, _ := breaker.Allow(" ninja "); allowed || !gotUntil.Equal(until) {
		t.Fatalf("blocked Allow = (%v, %v), want false and %v", allowed, gotUntil, until)
	}

	now = until
	if allowed, _, probe := breaker.Allow("NINJA"); !allowed || !probe {
		t.Fatalf("half-open Allow = (%v, %v), want allowed probe", allowed, probe)
	}
	if allowed, _, _ := breaker.Allow("Ninja"); allowed {
		t.Fatal("second half-open request was allowed while probe is in flight")
	}

	breaker.RecordSuccess("Ninja", true)
	if allowed, _, probe := breaker.Allow("Ninja"); !allowed || probe {
		t.Fatalf("Allow after successful probe = (%v, %v), want allowed non-probe", allowed, probe)
	}
}

func TestBreakerRepeatedRateLimitsBackOffExponentially(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	breaker := New()
	breaker.now = func() time.Time { return now }

	first := breaker.RecordRateLimit("Ninja", 0)
	now = first
	second := breaker.RecordRateLimit("Ninja", 0)
	if got := second.Sub(now); got != 2*time.Hour {
		t.Fatalf("second cooldown = %v, want 2h", got)
	}
}

func TestRetryHint(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	header := make(http.Header)
	header.Set("Retry-After", "120")
	if got := RetryHint(header, now); got != 2*time.Minute {
		t.Fatalf("seconds Retry-After = %v, want 2m", got)
	}

	header.Set("Retry-After", now.Add(3*time.Hour).Format(http.TimeFormat))
	if got := RetryHint(header, now); got != 3*time.Hour {
		t.Fatalf("date Retry-After = %v, want 3h", got)
	}
}
