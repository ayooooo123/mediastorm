package handlers

import (
	"testing"
	"time"

	"novastream/models"
)

func TestDashboardProgressUpdatedAtOmitsStaleResumeTimestamp(t *testing.T) {
	stale := time.Now().Add(-3 * time.Hour).UTC()
	progress := &models.PlaybackProgress{UpdatedAt: stale}

	if got, ok := dashboardProgressUpdatedAt(progress, false, time.Time{}, false); ok {
		t.Fatalf("stale progress returned interpolation anchor %v", got)
	}

	if got, ok := dashboardProgressUpdatedAt(progress, true, time.Time{}, false); !ok || !got.Equal(stale) {
		t.Fatalf("fresh progress anchor = %v, %v; want %v, true", got, ok, stale)
	}
}
