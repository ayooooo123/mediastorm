package epg

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
)

func TestFetchXtreamStreamsUsesUserAgentFallback(t *testing.T) {
	var userAgents []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgent := r.Header.Get("User-Agent")
		userAgents = append(userAgents, userAgent)
		if userAgent != xtreamBrowserUserAgent {
			http.Error(w, "unsupported client", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"stream_id":42,"epg_channel_id":"channel.test","name":"Test Channel"}]`)
	}))
	defer server.Close()

	service := &Service{client: server.Client()}
	settings := config.DefaultSettings()
	settings.Live.XtreamHost = server.URL
	settings.Live.XtreamUsername = "user"
	settings.Live.XtreamPassword = "pass"

	streams, err := service.fetchXtreamStreams(context.Background(), &settings)
	if err != nil {
		t.Fatalf("fetch Xtream streams: %v", err)
	}
	if len(streams) != 1 || streams[0].EPGChannelID != "channel.test" {
		t.Fatalf("streams = %+v, want one test channel", streams)
	}
	if len(userAgents) != 2 || userAgents[0] != xtreamStreamUserAgent || userAgents[1] != xtreamBrowserUserAgent {
		t.Fatalf("User-Agent attempts = %q, want VLC then browser", userAgents)
	}
}

func TestDecodeBase64Safe(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "valid base64",
			input:    base64.StdEncoding.EncodeToString([]byte("Hello World")),
			expected: "Hello World",
		},
		{
			name:     "not base64 - plain text",
			input:    "Just a plain title",
			expected: "Just a plain title",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "whitespace only",
			input:    "   ",
			expected: "",
		},
		{
			name:     "base64 with special chars",
			input:    base64.StdEncoding.EncodeToString([]byte("Café & Résumé")),
			expected: "Café & Résumé",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := decodeBase64Safe(tt.input)
			if result != tt.expected {
				t.Errorf("decodeBase64Safe(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMergePrograms(t *testing.T) {
	baseTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	makeProgram := func(title string, startOffset, stopOffset time.Duration) models.EPGProgram {
		return models.EPGProgram{
			ChannelID: "test.channel",
			Title:     title,
			Start:     baseTime.Add(startOffset),
			Stop:      baseTime.Add(stopOffset),
		}
	}

	t.Run("empty per-channel returns existing", func(t *testing.T) {
		existing := []models.EPGProgram{
			makeProgram("Show A", 0, 1*time.Hour),
		}
		result := mergePrograms(existing, nil)
		if len(result) != 1 || result[0].Title != "Show A" {
			t.Errorf("expected existing programs unchanged, got %d programs", len(result))
		}
	})

	t.Run("empty existing returns per-channel", func(t *testing.T) {
		perChannel := []models.EPGProgram{
			makeProgram("Show B", 0, 1*time.Hour),
		}
		result := mergePrograms(nil, perChannel)
		if len(result) != 1 || result[0].Title != "Show B" {
			t.Errorf("expected per-channel programs, got %d programs", len(result))
		}
	})

	t.Run("overlapping range replaces existing", func(t *testing.T) {
		existing := []models.EPGProgram{
			makeProgram("Old Morning", -2*time.Hour, -1*time.Hour), // before per-channel range
			makeProgram("Old Stale", 0, 1*time.Hour),               // overlaps with per-channel
			makeProgram("Old Stale 2", 1*time.Hour, 2*time.Hour),   // overlaps with per-channel
			makeProgram("Old Evening", 5*time.Hour, 6*time.Hour),   // after per-channel range
		}
		perChannel := []models.EPGProgram{
			makeProgram("Fresh Show 1", 0, 1*time.Hour),
			makeProgram("Fresh Show 2", 1*time.Hour, 3*time.Hour),
		}

		result := mergePrograms(existing, perChannel)

		// Should have: Old Morning, Fresh Show 1, Fresh Show 2, Old Evening
		if len(result) != 4 {
			t.Fatalf("expected 4 programs, got %d: %v", len(result), programTitles(result))
		}
		expectedTitles := []string{"Old Morning", "Fresh Show 1", "Fresh Show 2", "Old Evening"}
		for i, title := range expectedTitles {
			if result[i].Title != title {
				t.Errorf("program[%d]: expected %q, got %q", i, title, result[i].Title)
			}
		}
	})

	t.Run("no overlap keeps both", func(t *testing.T) {
		existing := []models.EPGProgram{
			makeProgram("Old Show", -2*time.Hour, -1*time.Hour),
		}
		perChannel := []models.EPGProgram{
			makeProgram("Fresh Show", 1*time.Hour, 2*time.Hour),
		}

		result := mergePrograms(existing, perChannel)
		if len(result) != 2 {
			t.Fatalf("expected 2 programs, got %d", len(result))
		}
		if result[0].Title != "Old Show" || result[1].Title != "Fresh Show" {
			t.Errorf("unexpected order: %v", programTitles(result))
		}
	})

	t.Run("deduplication", func(t *testing.T) {
		existing := []models.EPGProgram{
			makeProgram("Same Show", 0, 1*time.Hour),
		}
		perChannel := []models.EPGProgram{
			makeProgram("Same Show", 0, 1*time.Hour),
		}

		result := mergePrograms(existing, perChannel)
		// The existing one is within per-channel range so it gets replaced.
		// Per-channel has the same show. Should result in 1.
		if len(result) != 1 {
			t.Fatalf("expected 1 program after dedup, got %d", len(result))
		}
	})

	t.Run("sorted by start time", func(t *testing.T) {
		existing := []models.EPGProgram{
			makeProgram("Late Show", 10*time.Hour, 11*time.Hour),
		}
		perChannel := []models.EPGProgram{
			makeProgram("Early Show", 0, 1*time.Hour),
			makeProgram("Mid Show", 2*time.Hour, 3*time.Hour),
		}

		result := mergePrograms(existing, perChannel)
		for i := 1; i < len(result); i++ {
			if result[i].Start.Before(result[i-1].Start) {
				t.Errorf("programs not sorted: %q at %v before %q at %v",
					result[i-1].Title, result[i-1].Start, result[i].Title, result[i].Start)
			}
		}
	})
}

func TestNeedsXtreamPerChannelSupplement(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	t.Run("missing schedule does not supplement", func(t *testing.T) {
		if needsXtreamPerChannelSupplement(nil, "channel.one", now) {
			t.Fatal("nil schedule should not supplement")
		}
	})

	t.Run("missing channel programs supplements", func(t *testing.T) {
		schedule := &models.EPGSchedule{Programs: map[string][]models.EPGProgram{}}
		if !needsXtreamPerChannelSupplement(schedule, "channel.one", now) {
			t.Fatal("missing channel programs should supplement")
		}
	})

	t.Run("future programs skip supplement", func(t *testing.T) {
		schedule := &models.EPGSchedule{Programs: map[string][]models.EPGProgram{
			"channel.one": {
				{Title: "Now", Start: now.Add(-30 * time.Minute), Stop: now.Add(30 * time.Minute)},
			},
		}}
		if needsXtreamPerChannelSupplement(schedule, "CHANNEL.ONE", now) {
			t.Fatal("current/future programs should skip supplement")
		}
	})

	t.Run("stale programs supplement", func(t *testing.T) {
		schedule := &models.EPGSchedule{Programs: map[string][]models.EPGProgram{
			"channel.one": {
				{Title: "Old", Start: now.Add(-2 * time.Hour), Stop: now.Add(-1 * time.Hour)},
			},
		}}
		if !needsXtreamPerChannelSupplement(schedule, "channel.one", now) {
			t.Fatal("stale programs should supplement")
		}
	})
}

func programTitles(programs []models.EPGProgram) []string {
	titles := make([]string, len(programs))
	for i, p := range programs {
		titles[i] = p.Title
	}
	return titles
}
