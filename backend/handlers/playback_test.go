package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"novastream/models"
	"novastream/services/badstreams"
)

// mockPlaybackService implements the playbackService interface for testing.
type mockPlaybackService struct {
	resolveFunc      func(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error)
	resolveBatchFunc func(ctx context.Context, candidate models.NZBResult, episodes []models.BatchEpisodeTarget) (*models.BatchResolveResponse, error)
	queueStatusFunc  func(ctx context.Context, queueID int64) (*models.PlaybackResolution, error)
}

func (m *mockPlaybackService) Resolve(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
	if m.resolveFunc != nil {
		return m.resolveFunc(ctx, candidate)
	}
	return &models.PlaybackResolution{WebDAVPath: "/test"}, nil
}

func (m *mockPlaybackService) ResolveBatch(ctx context.Context, candidate models.NZBResult, episodes []models.BatchEpisodeTarget) (*models.BatchResolveResponse, error) {
	if m.resolveBatchFunc != nil {
		return m.resolveBatchFunc(ctx, candidate, episodes)
	}
	results := make([]models.BatchEpisodeResult, len(episodes))
	for i, ep := range episodes {
		results[i] = models.BatchEpisodeResult{
			SeasonNumber:  ep.SeasonNumber,
			EpisodeNumber: ep.EpisodeNumber,
			EpisodeCode:   ep.EpisodeCode,
			Resolution: &models.PlaybackResolution{
				WebDAVPath:   "/debrid/test/file",
				HealthStatus: "cached",
			},
		}
	}
	return &models.BatchResolveResponse{Results: results}, nil
}

func (m *mockPlaybackService) QueueStatus(ctx context.Context, queueID int64) (*models.PlaybackResolution, error) {
	if m.queueStatusFunc != nil {
		return m.queueStatusFunc(ctx, queueID)
	}
	return &models.PlaybackResolution{QueueID: queueID}, nil
}

func TestResolve_RejectsMarkedBadStreamByDefault(t *testing.T) {
	badStreamSvc := badstreams.New(filepath.Join(t.TempDir(), "bad_streams.json"))
	result := models.NZBResult{
		Title:       "Sample Release",
		ServiceType: models.ServiceTypeUsenet,
	}
	if _, err := badStreamSvc.Mark(badstreams.MarkRequest{
		ReleaseName: "Sample Release",
		ServiceType: string(models.ServiceTypeUsenet),
	}); err != nil {
		t.Fatalf("mark bad stream: %v", err)
	}

	resolveCalled := false
	h := NewPlaybackHandler(&mockPlaybackService{
		resolveFunc: func(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
			resolveCalled = true
			return &models.PlaybackResolution{WebDAVPath: "/test"}, nil
		},
	})
	h.SetBadStreamsService(badStreamSvc)

	body, _ := json.Marshal(map[string]interface{}{"result": result})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.Resolve(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d: %s", http.StatusConflict, rec.Code, rec.Body.String())
	}
	if resolveCalled {
		t.Fatal("expected marked bad stream to be rejected before resolve")
	}
}

func TestResolve_AllowsMarkedBadStreamWithManualOverride(t *testing.T) {
	badStreamSvc := badstreams.New(filepath.Join(t.TempDir(), "bad_streams.json"))
	result := models.NZBResult{
		Title:       "Sample Release",
		ServiceType: models.ServiceTypeUsenet,
	}
	if _, err := badStreamSvc.Mark(badstreams.MarkRequest{
		ReleaseName: "Sample Release",
		ServiceType: string(models.ServiceTypeUsenet),
	}); err != nil {
		t.Fatalf("mark bad stream: %v", err)
	}

	resolveCalled := false
	h := NewPlaybackHandler(&mockPlaybackService{
		resolveFunc: func(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
			resolveCalled = true
			return &models.PlaybackResolution{WebDAVPath: "/test"}, nil
		},
	})
	h.SetBadStreamsService(badStreamSvc)

	body, _ := json.Marshal(map[string]interface{}{
		"result":         result,
		"allowMarkedBad": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.Resolve(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !resolveCalled {
		t.Fatal("expected marked bad stream override to continue to resolve")
	}
}

func TestResolveBatch_MalformedJSON(t *testing.T) {
	h := NewPlaybackHandler(&mockPlaybackService{})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve-batch", bytes.NewBufferString(`{invalid`))
	rec := httptest.NewRecorder()
	h.ResolveBatch(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestResolveBatch_EmptyEpisodes(t *testing.T) {
	h := NewPlaybackHandler(&mockPlaybackService{})
	body, _ := json.Marshal(map[string]interface{}{
		"result":   models.NZBResult{Title: "Test"},
		"episodes": []models.BatchEpisodeTarget{},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve-batch", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.ResolveBatch(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestResolveBatch_OversizedBatch(t *testing.T) {
	h := NewPlaybackHandler(&mockPlaybackService{})
	episodes := make([]models.BatchEpisodeTarget, 101)
	for i := range episodes {
		episodes[i] = models.BatchEpisodeTarget{SeasonNumber: 1, EpisodeNumber: i + 1}
	}
	body, _ := json.Marshal(map[string]interface{}{
		"result":   models.NZBResult{Title: "Test"},
		"episodes": episodes,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve-batch", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.ResolveBatch(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestResolveBatch_Success(t *testing.T) {
	h := NewPlaybackHandler(&mockPlaybackService{})
	episodes := []models.BatchEpisodeTarget{
		{SeasonNumber: 1, EpisodeNumber: 1, EpisodeCode: "S01E01"},
		{SeasonNumber: 1, EpisodeNumber: 2, EpisodeCode: "S01E02"},
	}
	body, _ := json.Marshal(map[string]interface{}{
		"result":   models.NZBResult{Title: "Test", ServiceType: models.ServiceTypeDebrid},
		"episodes": episodes,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve-batch", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.ResolveBatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var resp models.BatchResolveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}

	for i, r := range resp.Results {
		if r.Resolution == nil {
			t.Errorf("result %d: nil resolution", i)
		} else if r.Resolution.WebDAVPath == "" {
			t.Errorf("result %d: empty webdav path", i)
		}
	}
}
