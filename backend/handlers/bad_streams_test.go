package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"novastream/services/badstreams"
)

func TestBadStreamsListFiltersAndPaginates(t *testing.T) {
	svc := badstreams.New(filepath.Join(t.TempDir(), "bad_streams.json"))
	for _, req := range []badstreams.MarkRequest{
		{
			ReleaseName: "Alpha.Release.2024.2160p",
			ServiceType: "debrid",
			Provider:    "realdebrid",
			SourcePath:  "/debrid/realdebrid/alpha.mkv",
			Reason:      "buffering",
		},
		{
			ReleaseName: "Beta.Release.2024.1080p",
			ServiceType: "debrid",
			Provider:    "alldebrid",
			SourcePath:  "/debrid/alldebrid/beta.mkv",
			Reason:      "open failed",
		},
		{
			ReleaseName: "Gamma.Release.2024.2160p",
			ServiceType: "debrid",
			Provider:    "realdebrid",
			SourcePath:  "/debrid/realdebrid/gamma.mkv",
			Reason:      "buffering",
		},
	} {
		if _, err := svc.Mark(req); err != nil {
			t.Fatalf("mark %q: %v", req.ReleaseName, err)
		}
	}

	handler := NewBadStreamsHandler(svc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/bad-streams?q=realdebrid&page=1&perPage=1", nil)
	handler.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp badStreamsListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Total != 2 || resp.Page != 1 || resp.PerPage != 1 || resp.TotalPages != 2 {
		t.Fatalf("pagination = total %d page %d perPage %d totalPages %d", resp.Total, resp.Page, resp.PerPage, resp.TotalPages)
	}
	if len(resp.Items) != 1 || len(resp.Streams) != 1 {
		t.Fatalf("items=%d streams=%d, want 1/1", len(resp.Items), len(resp.Streams))
	}
	if resp.Items[0].Provider != "realdebrid" {
		t.Fatalf("provider = %q, want realdebrid", resp.Items[0].Provider)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/admin/api/bad-streams?q=open&page=1&perPage=25", nil)
	handler.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Total != 1 || len(resp.Items) != 1 || resp.Items[0].Provider != "alldebrid" {
		t.Fatalf("filtered response = total %d items %#v", resp.Total, resp.Items)
	}
}
