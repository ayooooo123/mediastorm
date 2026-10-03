package epg

import (
	"bytes"
	"compress/gzip"
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

func TestHDHomeRunRefreshGzipFreshAuthCacheAndFailureRecovery(t *testing.T) {
	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	var discoveryCalls, guideCalls atomic.Int32
	var fail atomic.Bool
	now := time.Now().UTC()
	tuner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/discover.json" {
			t.Errorf("unexpected tuner path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]string{"DeviceAuth": fmt.Sprintf("secret+&%d", discoveryCalls.Add(1))})
	}))
	defer tuner.Close()
	guide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := guideCalls.Add(1)
		if r.URL.Query().Get("DeviceAuth") != fmt.Sprintf("secret+&%d", call) {
			t.Error("guide did not receive fresh encoded DeviceAuth")
		}
		if r.Header.Get("Accept-Encoding") != "gzip" {
			t.Error("guide request must accept gzip")
		}
		if fail.Load() {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		fmt.Fprintf(gz, `<tv><channel id="station-id"><display-name>5.1</display-name><display-name>TESTTV</display-name></channel><programme channel="station-id" start="%s" stop="%s"><title>Now</title></programme><programme channel="station-id" start="%s" stop="%s"><title>Day thirteen</title></programme></tv>`, now.Add(-time.Hour).Format("20060102150405 -0700"), now.Add(time.Hour).Format("20060102150405 -0700"), now.Add(13*24*time.Hour).Format("20060102150405 -0700"), now.Add(13*24*time.Hour+time.Hour).Format("20060102150405 -0700"))
	}))
	defer guide.Close()
	settings := config.DefaultSettings()
	settings.Live.EPG.Enabled = false
	settings.Live.Sources = []config.LivePlaylistSource{{ID: "tuner", Name: "Tuner", Mode: "hdhomerun", HDHomeRunHost: tuner.URL, EPG: config.EPGSettings{Enabled: true}}}
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := manager.Save(settings); err != nil {
		t.Fatal(err)
	}
	storage := t.TempDir()
	service := NewService(storage, manager)
	<-service.restoreDone
	service.hdHomeRunGuideURL = guide.URL
	if !service.HDHomeRunRefreshDue() {
		t.Fatal("initial guide not due")
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := service.GetStatus()
	if !status.Enabled || !service.IsEnabled() || status.ProgramCount != 2 || status.SourceCount != 1 {
		t.Fatalf("incorrect status or 14-day retention: %+v", status)
	}
	for _, id := range []string{"station-id", "TESTTV", "5.1"} {
		if programs := service.GetSchedule(id, now.Add(-time.Hour), now.Add(time.Hour)); len(programs) != 1 {
			t.Errorf("channel matching failed for %s", id)
		}
	}
	key, _ := config.HDHomeRunURL(tuner.URL, "/discover.json")
	service.hdHomeRunMu.Lock()
	cache := service.hdHomeRunCache[key]
	service.hdHomeRunMu.Unlock()
	delay := time.Until(cache.NextRefresh)
	if delay < 20*time.Hour-time.Minute || delay > 28*time.Hour {
		t.Fatalf("invalid randomized delay %s", delay)
	}
	data, err := os.ReadFile(service.hdHomeRunCachePath(key))
	if err != nil || strings.Contains(string(data), "secret+") {
		t.Fatal("cache missing or leaks auth")
	}
	if service.HDHomeRunRefreshDue() {
		t.Fatal("guide due before randomized deadline")
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if guideCalls.Load() != 1 {
		t.Fatal("early refresh ignored cache")
	}
	// A backend restart must reuse the guide and deadline, without downloading.
	restarted := NewService(storage, manager)
	<-restarted.restoreDone
	restarted.hdHomeRunGuideURL = guide.URL
	if err := restarted.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if guideCalls.Load() != 1 {
		t.Fatal("restart triggered premature download")
	}
	// Force the deadline to pass, then simulate a failed refresh.
	restarted.hdHomeRunMu.Lock()
	cache.NextRefresh = now.Add(-time.Second)
	restarted.hdHomeRunCache[key] = cache
	restarted.hdHomeRunMu.Unlock()
	fail.Store(true)
	if !restarted.HDHomeRunRefreshDue() {
		t.Fatal("expired guide not due")
	}
	if err := restarted.Refresh(context.Background()); err != nil {
		t.Fatal("cached guide should keep refresh usable", err)
	}
	status = restarted.GetStatus()
	if status.ProgramCount != 2 || status.LastError == "" || strings.Contains(status.LastError, "secret") {
		t.Fatalf("failure did not safely retain guide: %+v", status)
	}
	if restarted.HDHomeRunRefreshDue() {
		t.Fatal("failed guide should back off")
	}
	restarted.hdHomeRunMu.Lock()
	cache = restarted.hdHomeRunCache[key]
	cache.NextRefresh = now.Add(-time.Second)
	restarted.hdHomeRunCache[key] = cache
	restarted.hdHomeRunMu.Unlock()
	fail.Store(false)
	if err := restarted.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarted.GetStatus().LastError != "" || discoveryCalls.Load() != 3 || guideCalls.Load() != 3 {
		t.Fatal("guide did not recover using a fresh token")
	}
	for _, want := range []string{"[hdhomerun-epg] refresh start", "[hdhomerun-epg] cache reuse", "retainedCache=true", "skippedExistingProgramChannels=", "phase=\"download\"", "aliases=[\"5.1\" \"TESTTV\"]"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing diagnostic %q", want)
		}
	}
	for _, secret := range []string{"secret", "DeviceAuth=", "Day thirteen", "<tv>"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("logs leaked %q", secret)
		}
	}
}

func TestHDHomeRunProfileOnlyGuideIsEnabledAndScheduled(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Live.EPG.Enabled = false
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := manager.Save(settings); err != nil {
		t.Fatal(err)
	}
	service := NewService(t.TempDir(), manager)
	<-service.restoreDone
	enabled := true
	profile := models.LiveTVSettings{Sources: []models.LivePlaylistSource{{ID: "profile-tuner", Mode: "hdhomerun", HDHomeRunHost: "192.168.1.100", EPG: &models.LivePlaylistEPGSource{Enabled: &enabled}}}}
	service.SetProfileHDHomeRunSources(func(current config.Settings) []config.LivePlaylistSource {
		return ProfileHDHomeRunSources(profile, current.Live)
	})
	if !service.HasHDHomeRunGuide() || !service.IsEnabled() || !service.HDHomeRunRefreshDue() || service.GetStatus().SourceCount != 1 {
		t.Fatal("profile-only guide was not enabled or due")
	}
	disabled := false
	profile.Sources[0].EPG.Enabled = &disabled
	if service.HasHDHomeRunGuide() || service.HDHomeRunRefreshDue() {
		t.Fatal("disabled profile guide remains scheduled")
	}
}

func TestHDHomeRunDiscoveryMissingAuthDoesNotCallCloud(t *testing.T) {
	tuner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"DeviceID":"12345678"}`) }))
	defer tuner.Close()
	var called atomic.Bool
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called.Store(true) }))
	defer cloud.Close()
	service := &Service{client: tuner.Client(), hdHomeRunGuideURL: cloud.URL}
	schedule := &models.EPGSchedule{Channels: map[string]models.EPGChannel{}, Programs: map[string][]models.EPGProgram{}}
	if err := service.downloadHDHomeRunGuide(context.Background(), tuner.URL+"/discover.json", schedule); err == nil {
		t.Fatal("missing DeviceAuth accepted")
	}
	if called.Load() {
		t.Fatal("cloud called without DeviceAuth")
	}
}

func TestHDHomeRunSourcesRespectDisabledAndExplicitGuides(t *testing.T) {
	disabled := false
	settings := config.DefaultSettings()
	settings.Live.EPG.Enabled = false
	settings.Live.Sources = []config.LivePlaylistSource{
		{Mode: "hdhomerun", HDHomeRunHost: "tuner-a", EPG: config.EPGSettings{Enabled: true}},
		{Mode: "hdhomerun", HDHomeRunHost: "http://tuner-a/", EPG: config.EPGSettings{Enabled: true}},
		{Mode: "hdhomerun", HDHomeRunHost: "tuner-b", Enabled: &disabled, EPG: config.EPGSettings{Enabled: true}},
		{Mode: "hdhomerun", HDHomeRunHost: "tuner-c"},
		{Mode: "hdhomerun", HDHomeRunHost: "tuner-d", EPG: config.EPGSettings{Enabled: true, XmltvUrl: "http://guide/epg.xml"}},
	}
	if got := hdHomeRunSources(settings); len(got) != 1 || got[0].HDHomeRunHost != "tuner-a" {
		t.Fatalf("incorrect sources: %+v", got)
	}
	settings.Live.Sources = nil
	settings.Live.Mode = "hdhomerun"
	settings.Live.HDHomeRunHost = "legacy-tuner"
	settings.Live.EPG.Enabled = true
	if got := hdHomeRunSources(settings); len(got) != 1 {
		t.Fatal("legacy tuner config not supported")
	}
}
