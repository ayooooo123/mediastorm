package handlers

import (
	"errors"
	"testing"
	"time"

	nzbfilesystemlegacy "novastream/internal/nzbfilesystem"
	"novastream/internal/usenet"
	"novastream/services/debrid"
	"novastream/services/streaming"
)

func TestStreamFailureRegistryRecordsMissingArticleFailures(t *testing.T) {
	registry := &streamFailureRegistry{records: make(map[string]streamFailureRecord)}
	err := &nzbfilesystemlegacy.PartialContentError{
		BytesRead:     1024,
		TotalExpected: 2048,
		UnderlyingErr: &usenet.ArticleNotFoundError{
			UnderlyingErr: errors.New("article not found"),
			BytesRead:     1024,
		},
	}

	if !registry.recordIfMissingArticles("/webdav/movies/title.mkv", err) {
		t.Fatal("recordIfMissingArticles returned false")
	}

	record, ok := registry.confirmedRecent("movies/title.mkv", time.Minute)
	if !ok {
		t.Fatal("confirmedRecent returned false")
	}
	if record.Reason != "article_not_found" {
		t.Fatalf("reason = %q, want article_not_found", record.Reason)
	}
}

func TestStreamFailureRegistryRecordsProviderUnavailableFailures(t *testing.T) {
	registry := &streamFailureRegistry{records: make(map[string]streamFailureRecord)}
	err := errors.New("get torrent info failed: There was an error processing your request. Please try again later. (error: DATABASE_ERROR)")

	if !registry.recordIfMissingArticles("/debrid/torbox/123/file/0/title.mkv", err) {
		t.Fatal("recordIfMissingArticles returned false")
	}

	record, ok := registry.confirmedRecent("debrid/torbox/123/file/0/title.mkv", time.Minute)
	if !ok {
		t.Fatal("confirmedRecent returned false")
	}
	if record.Reason != "provider_unavailable" {
		t.Fatalf("reason = %q, want provider_unavailable", record.Reason)
	}
}

func TestStreamFailureRegistryRecordsDebridSourceFailures(t *testing.T) {
	registry := &streamFailureRegistry{records: make(map[string]streamFailureRecord)}
	err := &debrid.SourceError{
		Provider: "torbox",
		URL:      "https://nexus-124.wnam.tb-cdn.io/dld/file",
		Err:      errors.New("dial tcp 89.39.210.32:443: connect: connection refused"),
	}

	path := "/debrid/torbox/28584716/file/0/Alien3.mkv"
	if !registry.recordIfMissingArticles(path, err) {
		t.Fatal("recordIfMissingArticles returned false")
	}

	record, ok := registry.confirmedRecent("debrid/torbox/28584716/file/0/Alien3.mkv", time.Minute)
	if !ok {
		t.Fatal("confirmedRecent returned false")
	}
	if record.Reason != "provider_unavailable" {
		t.Fatalf("reason = %q, want provider_unavailable", record.Reason)
	}
}

func TestStreamFailureRegistryRecordsMissingStreamFailures(t *testing.T) {
	registry := &streamFailureRegistry{records: make(map[string]streamFailureRecord)}

	if !registry.recordIfMissingArticles("/webdav/stale/path/title.mkv", streaming.ErrNotFound) {
		t.Fatal("recordIfMissingArticles returned false")
	}

	record, ok := registry.confirmedRecent("stale/path/title.mkv", time.Minute)
	if !ok {
		t.Fatal("confirmedRecent returned false")
	}
	if record.Reason != "stream_not_found" {
		t.Fatalf("reason = %q, want stream_not_found", record.Reason)
	}
}
