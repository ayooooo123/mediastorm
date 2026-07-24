package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"novastream/models"
)

func TestParseEPGNowChannelIDs(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		target  string
		body    string
		want    []string
		wantErr bool
	}{
		{
			name:   "legacy GET query",
			method: http.MethodGet,
			target: "/api/live/epg/now?channels=ch1,%20ch2,ch1",
			want:   []string{"ch1", "ch2"},
		},
		{
			name:   "POST accepts long channel IDs in JSON body",
			method: http.MethodPost,
			target: "/api/live/epg/now?profileId=default",
			body:   `{"channels":["` + strings.Repeat("long/channel?id=&", 600) + `","ch2"]}`,
			want:   []string{strings.Repeat("long/channel?id=&", 600), "ch2"},
		},
		{
			name:    "POST rejects malformed JSON",
			method:  http.MethodPost,
			target:  "/api/live/epg/now",
			body:    `{"channels":`,
			wantErr: true,
		},
		{
			name:    "POST rejects empty channel list",
			method:  http.MethodPost,
			target:  "/api/live/epg/now",
			body:    `{"channels":[" "]}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
			got, err := parseEPGNowChannelIDs(httptest.NewRecorder(), request)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseEPGNowChannelIDs() error = %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseEPGNowChannelIDs() returned %d channels, want %d", len(got), len(tt.want))
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("channel %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestApplyOffsetToProgram(t *testing.T) {
	start := time.Date(2025, 1, 15, 20, 0, 0, 0, time.UTC)
	stop := time.Date(2025, 1, 15, 21, 0, 0, 0, time.UTC)

	prog := models.EPGProgram{
		ChannelID: "ch1",
		Title:     "Test Show",
		Start:     start,
		Stop:      stop,
	}

	// Positive offset: shift forward 30 minutes
	shifted := applyOffsetToProgram(prog, 30*time.Minute)
	if !shifted.Start.Equal(start.Add(30 * time.Minute)) {
		t.Errorf("Start = %v, want %v", shifted.Start, start.Add(30*time.Minute))
	}
	if !shifted.Stop.Equal(stop.Add(30 * time.Minute)) {
		t.Errorf("Stop = %v, want %v", shifted.Stop, stop.Add(30*time.Minute))
	}

	// Negative offset: shift backward 2 hours
	shifted = applyOffsetToProgram(prog, -2*time.Hour)
	if !shifted.Start.Equal(start.Add(-2 * time.Hour)) {
		t.Errorf("Start = %v, want %v", shifted.Start, start.Add(-2*time.Hour))
	}
	if !shifted.Stop.Equal(stop.Add(-2 * time.Hour)) {
		t.Errorf("Stop = %v, want %v", shifted.Stop, stop.Add(-2*time.Hour))
	}

	// Zero offset: no change
	shifted = applyOffsetToProgram(prog, 0)
	if !shifted.Start.Equal(start) {
		t.Errorf("Start = %v, want %v (unchanged)", shifted.Start, start)
	}

	// Original should be unmodified
	if !prog.Start.Equal(start) {
		t.Error("Original program was modified")
	}
}
