package epg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
)

func TestHDHomeRunForbiddenRetry(t *testing.T) {
	for _, test := range []struct {
		name           string
		status         int
		rotateAuth     bool
		failAgain      bool
		missingAuth    bool
		wantCalls      int32
		wantGuideCalls int32
		wantError      bool
	}{
		{name: "rotated auth recovers", status: 403, rotateAuth: true, wantCalls: 2, wantGuideCalls: 2},
		{name: "unchanged auth transient rejection", status: 403, wantCalls: 2, wantGuideCalls: 2},
		{name: "persistent rejection is bounded", status: 403, rotateAuth: true, failAgain: true, wantCalls: 2, wantGuideCalls: 2, wantError: true},
		{name: "missing retry auth stops cloud request", status: 403, missingAuth: true, wantCalls: 2, wantGuideCalls: 1, wantError: true},
		{name: "unavailable does not retry auth", status: 503, wantCalls: 1, wantGuideCalls: 1, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			previousOutput := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(previousOutput) })
			var discoveries, requests atomic.Int32
			tuner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := discoveries.Add(1)
				if test.missingAuth && call == 2 {
					fmt.Fprint(w, `{}`)
					return
				}
				token := "private-token+&1"
				if test.rotateAuth && call == 2 {
					token = "private-token+&2"
				}
				json.NewEncoder(w).Encode(map[string]string{"DeviceAuth": " " + token + " "})
			}))
			defer tuner.Close()
			now := time.Now().UTC()
			cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := requests.Add(1)
				wantAuth := "private-token+&1"
				if test.rotateAuth && call == 2 {
					wantAuth = "private-token+&2"
				}
				if r.URL.Query().Get("DeviceAuth") != wantAuth {
					t.Error("cloud did not receive freshly discovered, trimmed authentication")
				}
				if r.Header.Get("Accept-Encoding") != "gzip" || r.Header.Get("User-Agent") != "MediaStorm" {
					t.Error("cloud request missing gzip support or application identity")
				}
				if call == 1 || test.failAgain {
					// A reflected token in an error response must never enter logs.
					http.Error(w, wantAuth, test.status)
					return
				}
				fmt.Fprintf(w, `<tv><channel id="station"><display-name>TESTTV</display-name></channel><programme channel="station" start="%s" stop="%s"><title>Current</title></programme></tv>`, now.Add(-time.Hour).Format("20060102150405 -0700"), now.Add(time.Hour).Format("20060102150405 -0700"))
			}))
			defer cloud.Close()
			service := &Service{client: tuner.Client(), hdHomeRunGuideURL: cloud.URL}
			schedule := &models.EPGSchedule{Channels: map[string]models.EPGChannel{}, Programs: map[string][]models.EPGProgram{}}
			err := service.downloadHDHomeRunGuide(context.Background(), tuner.URL+"/discover.json", schedule, config.LivePlaylistSource{})
			if (err != nil) != test.wantError {
				t.Fatalf("download error=%v, want error=%v", err, test.wantError)
			}
			if discoveries.Load() != test.wantCalls || requests.Load() != test.wantGuideCalls {
				t.Fatalf("discovery requests=%d guide requests=%d", discoveries.Load(), requests.Load())
			}
			if !test.wantError && countSchedulePrograms(schedule) != 1 {
				t.Fatal("retry did not produce guide data")
			}
			if test.status == 403 && !strings.Contains(logs.String(), "retry=fresh-discovery") {
				t.Fatal("missing authentication retry diagnostic")
			}
			if test.rotateAuth && !strings.Contains(logs.String(), "authChanged=true") {
				t.Fatal("missing token rotation diagnostic")
			}
			if strings.Contains(logs.String(), "private-token") || (err != nil && strings.Contains(err.Error(), "private-token")) {
				t.Fatal("authentication leaked in diagnostics or errors")
			}
		})
	}
}

func TestHDHomeRunPersistentForbiddenRetainsGuideAndBacksOff(t *testing.T) {
	var discoveries, requests atomic.Int32
	tuner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		discoveries.Add(1)
		fmt.Fprint(w, `{"DeviceAuth":"private-token"}`)
	}))
	defer tuner.Close()
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer cloud.Close()
	storage := t.TempDir()
	if err := os.Mkdir(filepath.Join(storage, "epg"), 0700); err != nil {
		t.Fatal(err)
	}
	key := tuner.URL + "/discover.json"
	now := time.Now().UTC()
	cached := &models.EPGSchedule{Channels: map[string]models.EPGChannel{"station": {ID: "station"}}, Programs: map[string][]models.EPGProgram{"station": {{ChannelID: "station", Start: now.Add(-time.Hour), Stop: now.Add(time.Hour)}}}}
	service := &Service{client: tuner.Client(), storageDir: storage, hdHomeRunGuideURL: cloud.URL, hdHomeRunCache: map[string]hdHomeRunGuideCache{key: {Schedule: cached}}}
	source := config.LivePlaylistSource{Mode: "hdhomerun", HDHomeRunHost: tuner.URL}
	for i := 0; i < 2; i++ {
		target := &models.EPGSchedule{Channels: map[string]models.EPGChannel{}, Programs: map[string][]models.EPGProgram{}}
		if err := service.fetchHDHomeRunEPG(context.Background(), source, target); err == nil {
			t.Fatal("persistent 403 should return an error")
		}
		if countSchedulePrograms(target) != 1 {
			t.Fatal("previous working guide was not retained")
		}
	}
	if discoveries.Load() != 2 || requests.Load() != 2 {
		t.Fatal("backoff allowed another download after the bounded retry")
	}
	cache := service.hdHomeRunCache[key]
	if until := time.Until(cache.NextRefresh); until < 59*time.Minute || until > time.Hour {
		t.Fatalf("retry deadline = %s, want one hour", until)
	}
}
