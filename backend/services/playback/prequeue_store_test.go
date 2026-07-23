package playback

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"novastream/models"
)

func TestPrequeueStoreValidatesReadyEntryOnLookup(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	entry, created := store.Create("tvdb:series:353546", "Bluey", "default", "series", 2018, nil, "details")
	if !created {
		t.Fatal("Create returned created=false")
	}
	store.Update(entry.ID, func(e *PrequeueEntry) {
		e.Status = PrequeueStatusReady
		e.StreamPath = "/webdav/stale/title.mkv"
	})
	store.streamPathValidated = make(map[string]time.Time)

	var calls int32
	store.SetStreamPathValidator(func(ctx context.Context, streamPath string) error {
		atomic.AddInt32(&calls, 1)
		if streamPath != "/webdav/stale/title.mkv" {
			t.Fatalf("streamPath = %q, want /webdav/stale/title.mkv", streamPath)
		}
		return errors.New("stream not found")
	})

	if got, ok := store.GetByTitleUser("tvdb:series:353546", "default"); ok || got != nil {
		t.Fatalf("GetByTitleUser returned (%v, %t), want nil false", got, ok)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("validator calls = %d, want 1", calls)
	}
	if got, ok := store.Get(entry.ID); ok || got != nil {
		t.Fatalf("Get after validation failure returned (%v, %t), want nil false", got, ok)
	}
}

func TestPrequeueStoreKeepsValidReadyEntryOnLookup(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	entry, created := store.Create("movie:1", "Example", "default", "movie", 2024, nil, "details")
	if !created {
		t.Fatal("Create returned created=false")
	}
	store.Update(entry.ID, func(e *PrequeueEntry) {
		e.Status = PrequeueStatusReady
		e.StreamPath = "/webdav/valid/title.mkv"
	})
	store.streamPathValidated = make(map[string]time.Time)

	var calls int32
	store.SetStreamPathValidator(func(ctx context.Context, streamPath string) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})

	if got, ok := store.GetByTitleUser("movie:1", "default"); !ok || got == nil || got.ID != entry.ID {
		t.Fatalf("GetByTitleUser returned (%v, %t), want entry %s", got, ok, entry.ID)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("validator calls = %d, want 1", calls)
	}
	if got, ok := store.GetByTitleUser("movie:1", "default"); !ok || got == nil || got.ID != entry.ID {
		t.Fatalf("second GetByTitleUser returned (%v, %t), want entry %s", got, ok, entry.ID)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("validator calls after cached lookup = %d, want 1", calls)
	}
}

func TestPrequeueStoreDoesNotValidateNonReadyEntry(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	entry, created := store.Create("movie:1", "Example", "default", "movie", 2024, nil, "details")
	if !created {
		t.Fatal("Create returned created=false")
	}

	store.SetStreamPathValidator(func(ctx context.Context, streamPath string) error {
		t.Fatal("validator should not be called for non-ready entries")
		return nil
	})

	if got, ok := store.Get(entry.ID); !ok || got == nil {
		t.Fatalf("Get returned (%v, %t), want queued entry", got, ok)
	}
}

func TestPrequeueStoreWorkerCannotOverwriteAdoptedEntry(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	entry, created := store.Create("movie:1", "Example", "default", "movie", 2024, nil, "details")
	if !created {
		t.Fatal("Create returned created=false")
	}
	store.Update(entry.ID, func(e *PrequeueEntry) {
		e.Status = PrequeueStatusReady
		e.StreamPath = "/debrid/manual-selection.mkv"
		e.MigrationAdopted = true
	})

	if updated := store.UpdateWorker(entry.ID, func(e *PrequeueEntry) {
		e.Status = PrequeueStatusFailed
		e.StreamPath = "/downloads/stale-worker.mkv"
	}); updated {
		t.Fatal("UpdateWorker returned true for an adopted entry")
	}

	got, ok := store.Get(entry.ID)
	if !ok || got.StreamPath != "/debrid/manual-selection.mkv" || got.Status != PrequeueStatusReady {
		t.Fatalf("adopted entry was overwritten: %#v", got)
	}
}

func TestPrequeueEntryToResponseIncludesServiceType(t *testing.T) {
	entry := &PrequeueEntry{
		ID:          "pq_test",
		Status:      PrequeueStatusReady,
		StreamPath:  "/debrid/realdebrid/file.mkv",
		ServiceType: "debrid",
	}

	resp := entry.ToResponse()
	if resp.ServiceType != "debrid" {
		t.Fatalf("ServiceType = %q, want debrid", resp.ServiceType)
	}
}

func TestPrequeueEntryToResponseIncludesMigrationCandidates(t *testing.T) {
	entry := &PrequeueEntry{
		ID:                  "pq_test",
		Status:              PrequeueStatusReady,
		StreamPath:          "/downloads/usenet/file.mkv",
		SelectedResultIndex: 1,
		SelectedResult: &models.NZBResult{
			Title:   "Selected Release",
			Indexer: "indexer-b",
			GUID:    "guid-b",
		},
		MigrationCandidates: []models.NZBResult{
			{Title: "First Release", Indexer: "indexer-a", GUID: "guid-a"},
			{Title: "Selected Release", Indexer: "indexer-b", GUID: "guid-b"},
			{Title: "Next Release", Indexer: "indexer-c", GUID: "guid-c"},
		},
	}

	resp := entry.ToResponse()
	if resp.SelectedResult == nil || resp.SelectedResult.GUID != "guid-b" {
		t.Fatalf("SelectedResult = %#v, want guid-b", resp.SelectedResult)
	}
	if resp.SelectedResultIndex != 1 {
		t.Fatalf("SelectedResultIndex = %d, want 1", resp.SelectedResultIndex)
	}
	if len(resp.MigrationCandidates) != 3 {
		t.Fatalf("MigrationCandidates length = %d, want 3", len(resp.MigrationCandidates))
	}
}

func TestPrequeueEntryToResponseInfersServiceType(t *testing.T) {
	tests := []struct {
		name       string
		streamPath string
		want       string
	}{
		{name: "debrid path", streamPath: "/debrid/realdebrid/file.mkv", want: "debrid"},
		{name: "usenet path", streamPath: "/downloads/usenet/file.mkv", want: "usenet"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &PrequeueEntry{
				ID:         "pq_test",
				Status:     PrequeueStatusReady,
				StreamPath: tt.streamPath,
			}

			resp := entry.ToResponse()
			if resp.ServiceType != tt.want {
				t.Fatalf("ServiceType = %q, want %s", resp.ServiceType, tt.want)
			}
		})
	}
}
