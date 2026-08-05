package handlers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/services/streaming"
)

// mockProvider is a simple mock implementation of streaming.Provider for testing
type mockProviderWebDAV struct {
	data        []byte
	hadDeadline bool
}

func (m *mockProviderWebDAV) Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	_, m.hadDeadline = ctx.Deadline()
	headers := make(http.Header)
	headers.Set("Content-Type", "video/x-matroska")
	headers.Set("Accept-Ranges", "bytes")

	// Handle HEAD requests
	if req.Method == http.MethodHead {
		headers.Set("Content-Length", fmt.Sprintf("%d", len(m.data)))
		return &streaming.Response{
			Body:          nil,
			Headers:       headers,
			Status:        http.StatusOK,
			ContentLength: int64(len(m.data)),
		}, nil
	}

	// Handle range requests
	if req.RangeHeader != "" {
		// Parse range header (simple implementation for "bytes=0-4" format)
		var start, end int64
		if _, err := fmt.Sscanf(req.RangeHeader, "bytes=%d-%d", &start, &end); err == nil {
			if end >= int64(len(m.data)) {
				end = int64(len(m.data)) - 1
			}
			rangeData := m.data[start : end+1]
			headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(m.data)))
			headers.Set("Content-Length", fmt.Sprintf("%d", len(rangeData)))
			return &streaming.Response{
				Body:          io.NopCloser(bytes.NewReader(rangeData)),
				Headers:       headers,
				Status:        http.StatusPartialContent,
				ContentLength: int64(len(rangeData)),
			}, nil
		}
	}

	headers.Set("Content-Length", fmt.Sprintf("%d", len(m.data)))
	return &streaming.Response{
		Body:          io.NopCloser(bytes.NewReader(m.data)),
		Headers:       headers,
		Status:        http.StatusOK,
		ContentLength: int64(len(m.data)),
	}, nil
}

func newTestProvider(t *testing.T, data []byte) streaming.Provider {
	t.Helper()
	return &mockProviderWebDAV{data: data}
}

func TestWebDAVHandlerServeHTTP(t *testing.T) {
	data := []byte("hello world")
	provider := &mockProviderWebDAV{data: data}
	handler := NewWebDAVHandler(provider)

	req := httptest.NewRequest(http.MethodGet, "/webdav/movies/title.mkv", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	res := rr.Result()
	t.Cleanup(func() { _ = res.Body.Close() })

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Equal(body, data) {
		t.Fatalf("body = %q, want %q", body, data)
	}
	if provider.hadDeadline {
		t.Fatal("provider context has a deadline; WebDAV playback must remain open until the client disconnects")
	}
}

func TestWebDAVHandlerRangeRequest(t *testing.T) {
	data := []byte("hello world")
	handler := NewWebDAVHandler(newTestProvider(t, data))

	req := httptest.NewRequest(http.MethodGet, "/webdav/movies/title.mkv", nil)
	req.Header.Set("Range", "bytes=0-4")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	res := rr.Result()
	t.Cleanup(func() { _ = res.Body.Close() })

	if res.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusPartialContent)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(body) != "hello" {
		t.Fatalf("body = %q, want %q", body, "hello")
	}
}

func TestWebDAVHandlerHead(t *testing.T) {
	data := []byte("hello world")
	handler := NewWebDAVHandler(newTestProvider(t, data))

	req := httptest.NewRequest(http.MethodHead, "/webdav/movies/title.mkv", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	res := rr.Result()
	t.Cleanup(func() { _ = res.Body.Close() })

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}

	if cl := res.Header.Get("Content-Length"); cl == "" {
		t.Fatalf("Content-Length header missing")
	}
}
