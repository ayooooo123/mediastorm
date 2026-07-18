package debrid

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

type circuitTestProvider struct {
	mu    sync.Mutex
	calls int
	err   error
	wait  bool
}

func (p *circuitTestProvider) Name() string { return "torbox" }

func (p *circuitTestProvider) result(ctx context.Context) error {
	p.mu.Lock()
	p.calls++
	err := p.err
	wait := p.wait
	p.mu.Unlock()
	if wait {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (p *circuitTestProvider) AddMagnet(ctx context.Context, _ string) (*AddMagnetResult, error) {
	if err := p.result(ctx); err != nil {
		return nil, err
	}
	return &AddMagnetResult{ID: "1"}, nil
}

func (p *circuitTestProvider) AddTorrentFile(ctx context.Context, _ []byte, _ string) (*AddMagnetResult, error) {
	return p.AddMagnet(ctx, "")
}

func (p *circuitTestProvider) GetTorrentInfo(ctx context.Context, _ string) (*TorrentInfo, error) {
	if err := p.result(ctx); err != nil {
		return nil, err
	}
	return &TorrentInfo{Status: "downloaded"}, nil
}

func (p *circuitTestProvider) SelectFiles(ctx context.Context, _, _ string) error {
	return p.result(ctx)
}

func (p *circuitTestProvider) DeleteTorrent(ctx context.Context, _ string) error {
	return p.result(ctx)
}

func (p *circuitTestProvider) UnrestrictLink(ctx context.Context, _ string) (*UnrestrictResult, error) {
	if err := p.result(ctx); err != nil {
		return nil, err
	}
	return &UnrestrictResult{DownloadURL: "https://example.invalid/file"}, nil
}

func (p *circuitTestProvider) CheckInstantAvailability(ctx context.Context, _ string) (bool, error) {
	if err := p.result(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (p *circuitTestProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestResolutionCircuitFailsFastAndRecovers(t *testing.T) {
	now := time.Date(2026, 7, 17, 18, 0, 0, 0, time.UTC)
	circuit := newProviderResolutionCircuit()
	circuit.now = func() time.Time { return now }
	provider := &circuitTestProvider{err: &ProviderError{
		Provider:   "torbox",
		Operation:  "torrent info",
		StatusCode: http.StatusServiceUnavailable,
	}}
	wrapped := circuit.wrap(provider)

	if _, err := wrapped.GetTorrentInfo(context.Background(), "1"); err == nil {
		t.Fatal("first provider failure returned nil error")
	}
	provider.mu.Lock()
	provider.err = nil
	provider.mu.Unlock()

	if _, err := wrapped.GetTorrentInfo(context.Background(), "1"); err == nil {
		t.Fatal("open circuit returned nil error")
	} else {
		var openErr *ResolutionCircuitOpenError
		if !errors.As(err, &openErr) {
			t.Fatalf("open circuit error = %T, want ResolutionCircuitOpenError", err)
		}
	}
	if got := provider.callCount(); got != 1 {
		t.Fatalf("provider calls while circuit open = %d, want 1", got)
	}

	now = now.Add(defaultResolutionCooldown + time.Second)
	if _, err := wrapped.GetTorrentInfo(context.Background(), "1"); err != nil {
		t.Fatalf("half-open recovery probe failed: %v", err)
	}
	if _, err := wrapped.GetTorrentInfo(context.Background(), "1"); err != nil {
		t.Fatalf("closed circuit request failed: %v", err)
	}
	if got := provider.callCount(); got != 3 {
		t.Fatalf("provider calls after recovery = %d, want 3", got)
	}
}

func TestResolutionCircuitTimesOutProviderAPI(t *testing.T) {
	circuit := newProviderResolutionCircuit()
	circuit.apiTimeout = 20 * time.Millisecond
	provider := &circuitTestProvider{wait: true}
	wrapped := circuit.wrap(provider)

	started := time.Now()
	if _, err := wrapped.GetTorrentInfo(context.Background(), "1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("provider timeout took %v, want under 1s", elapsed)
	}
	if _, err := wrapped.GetTorrentInfo(context.Background(), "1"); err == nil {
		t.Fatal("second call should fail fast while circuit is open")
	}
	if got := provider.callCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
}

func TestResolutionCircuitDoesNotAmplifyConcurrentFailures(t *testing.T) {
	now := time.Date(2026, 7, 17, 18, 0, 0, 0, time.UTC)
	circuit := newProviderResolutionCircuit()
	circuit.now = func() time.Time { return now }
	err := &ProviderError{StatusCode: http.StatusServiceUnavailable}

	circuit.open("torbox", "first request", err)
	circuit.open("torbox", "already in flight request", err)

	circuit.mu.Lock()
	state := *circuit.states["torbox"]
	circuit.mu.Unlock()
	if state.failures != 1 {
		t.Fatalf("failure count = %d, want 1", state.failures)
	}
	if got := state.openUntil.Sub(now); got != defaultResolutionCooldown {
		t.Fatalf("cooldown = %v, want %v", got, defaultResolutionCooldown)
	}
}

func TestResolutionCircuitDoesNotOpenForItemFailure(t *testing.T) {
	circuit := newProviderResolutionCircuit()
	provider := &circuitTestProvider{err: errors.New("torrent not cached")}
	wrapped := circuit.wrap(provider)

	for i := 0; i < 2; i++ {
		if _, err := wrapped.GetTorrentInfo(context.Background(), "1"); err == nil {
			t.Fatal("item failure returned nil error")
		}
	}
	if got := provider.callCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
}

func TestProviderAPIUnavailableClassification(t *testing.T) {
	if !IsProviderAPIUnavailableError(context.DeadlineExceeded) {
		t.Fatal("deadline exceeded should be provider-unavailable")
	}
	if !IsProviderAPIUnavailableError(&ProviderError{StatusCode: http.StatusTooManyRequests}) {
		t.Fatal("HTTP 429 should be provider-unavailable")
	}
	if IsProviderAPIUnavailableError(errors.New("torbox authentication failed: invalid API key")) {
		t.Fatal("authentication failure should not open a temporary circuit")
	}
	if IsProviderAPIUnavailableError(errors.New("torrent not cached")) {
		t.Fatal("item-specific cache miss should not open provider circuit")
	}
}
