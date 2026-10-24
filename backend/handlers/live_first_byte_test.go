package handlers

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"novastream/config"
)

// blockingReader blocks every Read until it is closed.
type blockingReader struct{ closed chan struct{} }

func (b *blockingReader) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.ErrClosedPipe
}

func TestReadFirstLiveChunkReturnsData(t *testing.T) {
	buf := make([]byte, 16)
	n, err := readFirstLiveChunk(strings.NewReader("payload"), buf, time.Second, func() { t.Fatal("abort called") })
	if err != nil || string(buf[:n]) != "payload" {
		t.Fatalf("got %q, %v", buf[:n], err)
	}
}

func TestReadFirstLiveChunkTimesOutAndAborts(t *testing.T) {
	src := &blockingReader{closed: make(chan struct{})}
	aborted := false
	n, err := readFirstLiveChunk(src, make([]byte, 16), 50*time.Millisecond, func() {
		aborted = true
		close(src.closed)
	})
	if n != 0 || !errors.Is(err, errLiveFirstByteTimeout) || !aborted {
		t.Fatalf("n=%d err=%v aborted=%v", n, err, aborted)
	}
}

func TestLiveDirectFirstByteDeadlineAddsProbe(t *testing.T) {
	if got := liveDirectFirstByteDeadline(0); got != liveDirectFirstByteTimeout {
		t.Fatalf("deadline(0) = %v", got)
	}
	if got := liveDirectFirstByteDeadline(15); got != liveDirectFirstByteTimeout+15*time.Second {
		t.Fatalf("deadline(15) = %v", got)
	}
}

// A provider that answers but never sends used to leave the client on a 200 with no body.
func TestStreamChannelSilentUpstreamReturnsGatewayTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg shell script is POSIX-only")
	}
	previous := liveDirectFirstByteTimeout
	liveDirectFirstByteTimeout = 200 * time.Millisecond
	defer func() { liveDirectFirstByteTimeout = previous }()

	release := make(chan struct{})
	defer close(release)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer proxyServer.Close()

	for _, tc := range []struct {
		name   string
		target string
	}{
		{"transmux", "web"},
		{"passthrough", "native"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Fake ffmpeg: copy stdin to stdout, so it stays as silent as the provider.
			scriptPath := filepath.Join(t.TempDir(), "fake-ffmpeg.sh")
			if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexec cat\n"), 0o755); err != nil {
				t.Fatalf("write fake ffmpeg: %v", err)
			}
			mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
			if err := mgr.Save(config.Settings{Live: config.LiveSettings{
				Mode: "xtream", XtreamHost: "http://192.0.2.1", XtreamUsername: "user", XtreamPassword: "pass",
				ProxyURL: proxyServer.URL,
			}}); err != nil {
				t.Fatalf("save settings: %v", err)
			}
			h := NewLiveHandler(proxyServer.Client(), true, scriptPath, 24, 0, 0, false, mgr, nil)

			req := httptest.NewRequest(http.MethodGet, "/live/stream?target="+tc.target+"&url=http://192.0.2.1/live/user/pass/1.ts", nil)
			rec := httptest.NewRecorder()
			started := time.Now()
			h.StreamChannel(rec, req)

			if rec.Code != http.StatusGatewayTimeout {
				t.Fatalf("status = %d, want %d (body=%q)", rec.Code, http.StatusGatewayTimeout, rec.Body.String())
			}
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Fatalf("took %v; the deadline did not cut the silent stream off", elapsed)
			}
		})
	}
}
