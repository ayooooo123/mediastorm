package pool

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/javi11/nntppool/v2"
	"github.com/javi11/nntppool/v2/pkg/nntpcli"
)

func TestSetProvidersReturnsBeforePoolBuildCompletes(t *testing.T) {
	buildStarted := make(chan struct{})
	releaseBuild := make(chan struct{})
	want := &stubPool{quit: make(chan struct{})}
	m := &manager{newPool: func(config nntppool.Config) (nntppool.UsenetConnectionPool, error) {
		close(buildStarted)
		<-releaseBuild
		return want, nil
	}}

	returned := make(chan struct{})
	go func() {
		_ = m.SetProviders([]nntppool.UsenetProviderConfig{{Host: "news.example.com"}})
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("SetProviders blocked on pool construction")
	}
	<-buildStarted
	if m.HasPool() {
		t.Fatal("pool was available before verification completed")
	}

	close(releaseBuild)
	waitFor(t, time.Second, func() bool {
		got, err := m.GetPool()
		return err == nil && got == want
	})
}

func TestSetProvidersDiscardsSupersededBuild(t *testing.T) {
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	first := &stubPool{quit: make(chan struct{})}
	second := &stubPool{quit: make(chan struct{})}
	var mu sync.Mutex
	build := 0
	m := &manager{newPool: func(config nntppool.Config) (nntppool.UsenetConnectionPool, error) {
		mu.Lock()
		build++
		current := build
		mu.Unlock()
		if current == 1 {
			<-firstRelease
			return first, nil
		}
		<-secondRelease
		return second, nil
	}}

	_ = m.SetProviders([]nntppool.UsenetProviderConfig{{Host: "old.example.com"}})
	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return build == 1
	})
	_ = m.SetProviders([]nntppool.UsenetProviderConfig{{Host: "new.example.com"}})
	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return build == 2
	})

	close(secondRelease)
	waitFor(t, time.Second, func() bool {
		got, err := m.GetPool()
		return err == nil && got == second
	})
	close(firstRelease)

	select {
	case <-first.quit:
	case <-time.After(time.Second):
		t.Fatal("superseded pool was not shut down")
	}
	got, err := m.GetPool()
	if err != nil || got != second {
		t.Fatalf("superseded build replaced current pool: pool=%v err=%v", got, err)
	}
}

func TestClearPoolInvalidatesBuildInProgress(t *testing.T) {
	releaseBuild := make(chan struct{})
	built := &stubPool{quit: make(chan struct{})}
	m := &manager{newPool: func(config nntppool.Config) (nntppool.UsenetConnectionPool, error) {
		<-releaseBuild
		return built, nil
	}}

	_ = m.SetProviders([]nntppool.UsenetProviderConfig{{Host: "news.example.com"}})
	if err := m.ClearPool(); err != nil {
		t.Fatalf("ClearPool() error = %v", err)
	}
	close(releaseBuild)

	select {
	case <-built.quit:
	case <-time.After(time.Second):
		t.Fatal("pool completed after clear was not shut down")
	}
	if m.HasPool() {
		t.Fatal("pool became available after it was cleared")
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

type stubPool struct {
	quit chan struct{}
	once sync.Once
}

func (p *stubPool) GetConnection(context.Context, []string, bool) (nntppool.PooledConnection, error) {
	return nil, nil
}
func (p *stubPool) Body(context.Context, string, io.Writer, []string) (int64, error) {
	return 0, nil
}
func (p *stubPool) BodyReader(context.Context, string, []string) (nntpcli.ArticleBodyReader, error) {
	return nil, nil
}
func (p *stubPool) Post(context.Context, io.Reader) error                   { return nil }
func (p *stubPool) Stat(context.Context, string, []string) (int, error)     { return 0, nil }
func (p *stubPool) GetProvidersInfo() []nntppool.ProviderInfo               { return nil }
func (p *stubPool) GetProviderStatus(string) (*nntppool.ProviderInfo, bool) { return nil, false }
func (p *stubPool) BodyBatch(context.Context, string, []nntppool.BodyBatchRequest) []nntppool.BodyBatchResult {
	return nil
}
func (p *stubPool) TestProviderPipelineSupport(context.Context, string, string) (bool, int, error) {
	return false, 0, nil
}
func (p *stubPool) GetMetrics() *nntppool.PoolMetrics { return nil }
func (p *stubPool) GetMetricsSnapshot() nntppool.PoolMetricsSnapshot {
	return nntppool.PoolMetricsSnapshot{}
}
func (p *stubPool) Quit() { p.once.Do(func() { close(p.quit) }) }
