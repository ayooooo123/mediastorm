package peartube

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Seeds a local file into a relay through the same client path the admin
// "Seed" action uses, then waits for the job to finish.
func TestLiveRelaySeedsLocalFile(t *testing.T) {
	base := strings.TrimSpace(os.Getenv("PEARTUBE_LIVE_RELAY"))
	path := strings.TrimSpace(os.Getenv("PEARTUBE_SEED_FILE"))
	if base == "" || path == "" {
		t.Skip("set PEARTUBE_LIVE_RELAY and PEARTUBE_SEED_FILE")
	}
	client, err := New(base)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	job, err := client.Archive(ctx, ArchiveRequest{
		FilePath: path,
		ArchiveCoordinates: ArchiveCoordinates{
			ContentKind: "movie",
			TMDBID:      "27205",
			TMDBTitle:   "Inception",
		},
	})
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	t.Logf("job %s status %s entity %s", job.JobID, job.Status, job.EntityHint)

	for deadline := time.Now().Add(4 * time.Minute); time.Now().Before(deadline); {
		status, err := client.ArchiveStatus(ctx, job.JobID)
		if err != nil {
			t.Fatalf("ArchiveStatus: %v", err)
		}
		if status.Status == "completed" {
			t.Logf("seeded: %+v", status)
			return
		}
		if status.Status == "failed" {
			t.Fatalf("seed failed: %s", status.Error)
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("seed job never completed")
}
