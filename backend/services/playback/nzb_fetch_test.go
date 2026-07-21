package playback

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/internal/httpheaders"
	"novastream/internal/providerbreaker"
	"novastream/models"
)

func TestFetchNZBSetsDownloadHeaders(t *testing.T) {
	var receivedUserAgent string
	var receivedAccept string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUserAgent = r.Header.Get("User-Agent")
		receivedAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Disposition", `attachment; filename="test.nzb"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><nzb></nzb>`))
	}))
	defer server.Close()

	svc := &Service{httpClient: server.Client()}
	_, _, err := svc.fetchNZB(context.Background(), server.URL+"/test.nzb", models.NZBResult{Title: "Test"})
	if err != nil {
		t.Fatalf("fetchNZB returned error: %v", err)
	}
	if receivedUserAgent != httpheaders.NZBDownloadUserAgent {
		t.Fatalf("User-Agent = %q, want %q", receivedUserAgent, httpheaders.NZBDownloadUserAgent)
	}
	if receivedAccept == "" {
		t.Fatal("expected Accept header to be set")
	}
}

func TestFetchNZBRateLimitSkipsProviderAndAllowsFallback(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="test.nzb"`)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><nzb></nzb>`))
	}))
	defer server.Close()

	svc := &Service{httpClient: server.Client(), providerBreaker: providerbreaker.New()}
	ninja := models.NZBResult{Title: "Ninja result", Indexer: "Ninja"}
	if _, _, err := svc.fetchNZB(context.Background(), server.URL+"/ninja-1", ninja); err == nil {
		t.Fatal("first Ninja request returned nil error for 429")
	}
	if _, _, err := svc.fetchNZB(context.Background(), server.URL+"/ninja-2", ninja); err == nil {
		t.Fatal("second Ninja request was not blocked")
	}
	if requests != 1 {
		t.Fatalf("Ninja HTTP requests = %d, want 1", requests)
	}

	fallback := models.NZBResult{Title: "Geek result", Indexer: "NZBGeek"}
	if _, _, err := svc.fetchNZB(context.Background(), server.URL+"/geek", fallback); err != nil {
		t.Fatalf("fallback provider failed: %v", err)
	}
	if requests != 2 {
		t.Fatalf("total HTTP requests = %d, want 2", requests)
	}
}
