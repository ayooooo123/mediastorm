package playback

import (
	"testing"
	"time"

	"novastream/models"
)

func TestDynamicTTL_Tiers(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name        string
		airDateUTC  string // RFC3339
		airDate     string // YYYY-MM-DD
		year        int
		mediaType   string
		expectedTTL time.Duration
	}{
		// Tier 1: >1h before airtime → 12h
		{
			name:        "8 days before air date",
			airDateUTC:  now.Add(8 * 24 * time.Hour).Format(time.RFC3339),
			expectedTTL: 12 * time.Hour,
		},
		{
			name:        "2 days before air date",
			airDateUTC:  now.Add(2 * 24 * time.Hour).Format(time.RFC3339),
			expectedTTL: 12 * time.Hour,
		},
		{
			name:        "3 hours before air date",
			airDateUTC:  now.Add(3 * time.Hour).Format(time.RFC3339),
			expectedTTL: 12 * time.Hour,
		},

		// Tier 2: 1h before to 2h after → 15min
		{
			name:        "30 minutes before air date",
			airDateUTC:  now.Add(30 * time.Minute).Format(time.RFC3339),
			expectedTTL: 15 * time.Minute,
		},
		{
			name:        "exactly at air time",
			airDateUTC:  now.Format(time.RFC3339),
			expectedTTL: 15 * time.Minute,
		},
		{
			name:        "30 minutes after air date",
			airDateUTC:  now.Add(-30 * time.Minute).Format(time.RFC3339),
			expectedTTL: 15 * time.Minute,
		},
		{
			name:        "1.5 hours after air date",
			airDateUTC:  now.Add(-90 * time.Minute).Format(time.RFC3339),
			expectedTTL: 15 * time.Minute,
		},

		// Tier 3: 2h to 24h after → 1h
		{
			name:        "3 hours after air date",
			airDateUTC:  now.Add(-3 * time.Hour).Format(time.RFC3339),
			expectedTTL: 1 * time.Hour,
		},
		{
			name:        "12 hours after air date",
			airDateUTC:  now.Add(-12 * time.Hour).Format(time.RFC3339),
			expectedTTL: 1 * time.Hour,
		},

		// Tier 4: >24h after → 24h
		{
			name:        "2 days after air date",
			airDateUTC:  now.Add(-2 * 24 * time.Hour).Format(time.RFC3339),
			expectedTTL: 24 * time.Hour,
		},
		{
			name:        "5 weeks after air date",
			airDateUTC:  now.Add(-35 * 24 * time.Hour).Format(time.RFC3339),
			expectedTTL: 24 * time.Hour,
		},

		// Year-based fallbacks
		{
			name:        "current year content",
			year:        now.Year(),
			mediaType:   "movie",
			expectedTTL: 1 * time.Hour,
		},
		{
			name:        "older content",
			year:        2020,
			mediaType:   "movie",
			expectedTTL: 24 * time.Hour,
		},

		// No date info
		{
			name:        "no date info",
			expectedTTL: 1 * time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DynamicTTL(tt.airDate, tt.airDateUTC, tt.year, tt.mediaType)
			if got != tt.expectedTTL {
				t.Errorf("DynamicTTL() = %v, want %v", got, tt.expectedTTL)
			}
		})
	}
}

func TestDynamicTTL_AirDateTimeUTCPreferredOverAirDate(t *testing.T) {
	now := time.Now()

	// airDateTimeUTC says 30min from now (should be 15min TTL — in the hot window)
	// airDate says 5 days ago (should be 24h TTL)
	airDateUTC := now.Add(30 * time.Minute).Format(time.RFC3339)
	airDate := now.Add(-5 * 24 * time.Hour).Format("2006-01-02")

	got := DynamicTTL(airDate, airDateUTC, 0, "series")
	if got != 15*time.Minute {
		t.Errorf("expected 15m (from airDateTimeUTC), got %v", got)
	}
}

func TestDynamicTTL_AirDateFallback(t *testing.T) {
	now := time.Now()

	// Only airDate set, 2 days ago (noon UTC assumption → ~2 days after → >24h → 24h tier)
	airDate := now.Add(-2 * 24 * time.Hour).Format("2006-01-02")

	got := DynamicTTL(airDate, "", 0, "series")
	if got != 24*time.Hour {
		t.Errorf("expected 24h for 2 days after air date, got %v", got)
	}
}

func TestDynamicTTL_BoundaryExactly1HourBefore(t *testing.T) {
	now := time.Now()

	// Exactly 1 hour before → distance is -1h → hours < -1, so 12h
	airDateUTC := now.Add(1*time.Hour + 1*time.Second).Format(time.RFC3339)
	got := DynamicTTL("", airDateUTC, 0, "series")
	if got != 12*time.Hour {
		t.Errorf("expected 12h just over 1h before, got %v", got)
	}
}

func TestDynamicTTL_BoundaryExactly2HoursAfter(t *testing.T) {
	now := time.Now()

	// Just over 2 hours after → should be 1h tier
	airDateUTC := now.Add(-2*time.Hour - 1*time.Second).Format(time.RFC3339)
	got := DynamicTTL("", airDateUTC, 0, "series")
	if got != 1*time.Hour {
		t.Errorf("expected 1h just over 2h after, got %v", got)
	}
}

func TestDynamicTTL_BoundaryExactly24HoursAfter(t *testing.T) {
	now := time.Now()

	// Just over 24 hours after → should be 24h tier
	airDateUTC := now.Add(-24*time.Hour - 1*time.Second).Format(time.RFC3339)
	got := DynamicTTL("", airDateUTC, 0, "series")
	if got != 24*time.Hour {
		t.Errorf("expected 24h just over 24h after, got %v", got)
	}
}

func TestPrequeueEntry_DynamicTTL(t *testing.T) {
	now := time.Now()

	entry := &PrequeueEntry{
		MediaType: "series",
		Year:      2024,
		TargetEpisode: &models.EpisodeReference{
			AirDateTimeUTC: now.Add(-90 * time.Minute).Format(time.RFC3339),
		},
	}

	got := entry.DynamicTTL()
	if got != 15*time.Minute {
		t.Errorf("expected 15m for 1.5h after air, got %v", got)
	}
}

func TestPrequeueEntry_DynamicTTL_NilEpisode(t *testing.T) {
	entry := &PrequeueEntry{
		MediaType: "movie",
		Year:      2020,
	}

	got := entry.DynamicTTL()
	if got != 24*time.Hour {
		t.Errorf("expected 24h for older movie, got %v", got)
	}
}
