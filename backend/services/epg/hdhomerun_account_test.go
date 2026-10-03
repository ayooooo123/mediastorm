package epg

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
)

func TestHDHomeRunAccountGuideSkipsDiscovery(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var logs bytes.Buffer
			previousOutput := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(previousOutput) })
			var discoveryCalls, guideCalls atomic.Int32
			tuner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				discoveryCalls.Add(1)
				http.Error(w, "tuner unavailable", http.StatusServiceUnavailable)
			}))
			defer tuner.Close()
			now := time.Now().UTC()
			cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				guideCalls.Add(1)
				query := r.URL.Query()
				if query.Get("Email") != "private+guide@example.com" || query.Get("DeviceIDs") != "10AFFFFF,10BFFFFF" || query.Has("DeviceAuth") {
					t.Error("account guide request used incorrect authentication parameters")
				}
				if r.Header.Get("Accept-Encoding") != "gzip" || r.Header.Get("User-Agent") != "MediaStorm" {
					t.Error("account guide request lost required headers")
				}
				if status != http.StatusOK {
					http.Error(w, "private+guide@example.com 10AFFFFF", status)
					return
				}
				w.Header().Set("Content-Encoding", "gzip")
				gz := gzip.NewWriter(w)
				defer gz.Close()
				fmt.Fprintf(gz, `<tv><channel id="station"><display-name>5.1</display-name><display-name>TESTTV</display-name></channel><programme channel="station" start="%s" stop="%s"><title>Current</title></programme></tv>`, now.Add(-time.Hour).Format("20060102150405 -0700"), now.Add(time.Hour).Format("20060102150405 -0700"))
			}))
			defer cloud.Close()
			source := config.LivePlaylistSource{Mode: "hdhomerun", HDHomeRunHost: tuner.URL, HDHomeRunGuideEmail: " private+guide@example.com ", HDHomeRunGuideDeviceIDs: "10bfffff, 10AFFFFF,10bfffff"}
			service := &Service{client: tuner.Client(), hdHomeRunGuideURL: cloud.URL}
			schedule := &models.EPGSchedule{Channels: map[string]models.EPGChannel{}, Programs: map[string][]models.EPGProgram{}}
			err := service.downloadHDHomeRunGuide(context.Background(), tuner.URL+"/discover.json", schedule, source)
			if (err == nil) != (status == http.StatusOK) || discoveryCalls.Load() != 0 || guideCalls.Load() != 1 {
				t.Fatalf("err=%v discoveryCalls=%d guideCalls=%d", err, discoveryCalls.Load(), guideCalls.Load())
			}
			if status == http.StatusOK && countSchedulePrograms(schedule) != 1 {
				t.Fatal("account guide was not imported")
			}
			output := logs.String() + hdHomeRunSourceKey(source) + service.hdHomeRunCachePath(hdHomeRunSourceKey(source))
			if err != nil {
				output += err.Error()
			}
			for _, identifier := range []string{"private+guide@example.com", "10AFFFFF", "10BFFFFF", "Email="} {
				if strings.Contains(output, identifier) {
					t.Fatal("account identifiers leaked into diagnostics or cache identity")
				}
			}
			if !strings.Contains(logs.String(), "authMode=email-deviceids deviceCount=2") {
				t.Fatal("missing account authentication diagnostic")
			}
		})
	}
}

func TestHDHomeRunIncompleteAccountDoesNotFallBack(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	service := &Service{client: server.Client(), hdHomeRunGuideURL: server.URL}
	source := config.LivePlaylistSource{HDHomeRunGuideEmail: "private@example.com"}
	schedule := &models.EPGSchedule{Channels: map[string]models.EPGChannel{}, Programs: map[string][]models.EPGProgram{}}
	if err := service.downloadHDHomeRunGuide(context.Background(), server.URL+"/discover.json", schedule, source); err == nil || calls.Load() != 0 {
		t.Fatal("incomplete account settings should fail without calling tuner or cloud")
	}
}

func TestHDHomeRunAccountChangeInvalidatesCacheDeadline(t *testing.T) {
	settings := config.DefaultSettings()
	source := config.LivePlaylistSource{Mode: "hdhomerun", HDHomeRunHost: "tuner.local", HDHomeRunGuideEmail: "private@example.com", HDHomeRunGuideDeviceIDs: "10AFFFFF", EPG: config.EPGSettings{Enabled: true}}
	settings.Live.Sources = []config.LivePlaylistSource{source}
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := manager.Save(settings); err != nil {
		t.Fatal(err)
	}
	service := NewService(t.TempDir(), manager)
	<-service.restoreDone
	service.hdHomeRunCache = map[string]hdHomeRunGuideCache{hdHomeRunSourceKey(source): {NextRefresh: time.Now().Add(24 * time.Hour)}}
	if service.HDHomeRunRefreshDue() {
		t.Fatal("unchanged account guide should reuse its deadline")
	}
	for _, change := range []func(*config.LivePlaylistSource){
		func(s *config.LivePlaylistSource) { s.HDHomeRunGuideEmail = "other@example.com" },
		func(s *config.LivePlaylistSource) { s.HDHomeRunGuideDeviceIDs = "10BFFFFF" },
		func(s *config.LivePlaylistSource) { s.HDHomeRunGuideEmail, s.HDHomeRunGuideDeviceIDs = "", "" },
	} {
		next := source
		change(&next)
		settings.Live.Sources[0] = next
		if err := manager.Save(settings); err != nil {
			t.Fatal(err)
		}
		if !service.HDHomeRunRefreshDue() {
			t.Fatal("authentication change reused the old cache deadline")
		}
	}
	if hdHomeRunSourceKey(config.LivePlaylistSource{HDHomeRunHost: "tuner.local"}) != "http://tuner.local/discover.json" {
		t.Fatal("automatic authentication lost its legacy cache identity")
	}
}

func TestHDHomeRunGuideAccountSourceResolution(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Live.Mode = "hdhomerun"
	settings.Live.HDHomeRunHost = "tuner.local"
	settings.Live.HDHomeRunGuideEmail = "viewer@example.com"
	settings.Live.HDHomeRunGuideDeviceIDs = "10AFFFFF,10BFFFFF"
	settings.Live.EPG.Enabled = true
	sources := hdHomeRunSources(settings)
	if len(sources) != 1 || sources[0].HDHomeRunGuideEmail != settings.Live.HDHomeRunGuideEmail || sources[0].HDHomeRunGuideDeviceIDs != settings.Live.HDHomeRunGuideDeviceIDs {
		t.Fatal("legacy global account settings were not propagated")
	}
	email, ids := "profile@example.com", "10CFFFFF"
	profile := models.LiveTVSettings{HDHomeRunGuideEmail: &email, HDHomeRunGuideDeviceIDs: &ids}
	sources = ProfileHDHomeRunSources(profile, settings.Live)
	if len(sources) != 1 || sources[0].HDHomeRunHost != "tuner.local" || sources[0].HDHomeRunGuideEmail != email || sources[0].HDHomeRunGuideDeviceIDs != ids {
		t.Fatal("profile account override with inherited tuner address was not propagated")
	}
	profile.Sources = []models.LivePlaylistSource{{Mode: "hdhomerun", HDHomeRunHost: "other.local", HDHomeRunGuideEmail: email, HDHomeRunGuideDeviceIDs: ids}}
	sources = ProfileHDHomeRunSources(profile, settings.Live)
	if len(sources) != 1 || sources[0].HDHomeRunHost != "other.local" || sources[0].HDHomeRunGuideEmail != email || sources[0].HDHomeRunGuideDeviceIDs != ids {
		t.Fatal("profile per-source account settings were not propagated")
	}
	a := config.LivePlaylistSource{Mode: "hdhomerun", HDHomeRunHost: "tuner.local", HDHomeRunGuideEmail: email, HDHomeRunGuideDeviceIDs: "10AFFFFF,10BFFFFF", EPG: config.EPGSettings{Enabled: true}}
	b := a
	b.HDHomeRunGuideDeviceIDs = "10bfffff, 10afffff,10AFFFFF"
	settings.Live.Sources = []config.LivePlaylistSource{a, b}
	if len(hdHomeRunSources(settings)) != 1 {
		t.Fatal("equivalent device ID lists were not deduplicated")
	}
	b.HDHomeRunGuideDeviceIDs = "10CFFFFF"
	settings.Live.Sources[1] = b
	if len(hdHomeRunSources(settings)) != 2 {
		t.Fatal("distinct account guide configurations were merged")
	}
}
