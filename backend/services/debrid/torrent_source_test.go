package debrid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
)

const redirectMagnet = "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=Big+Brother+US&tr=udp%3A%2F%2Ftracker.example%3A80"

func newMagnetRedirectServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/download" {
			http.Redirect(w, r, "/magnet", http.StatusFound)
			return
		}
		http.Redirect(w, r, redirectMagnet, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type sourceTestProvider struct {
	*mockProvider
	magnet   string
	uploads  int
	data     []byte
	filename string
}

func (p *sourceTestProvider) AddMagnet(ctx context.Context, magnet string) (*AddMagnetResult, error) {
	p.magnet = magnet
	return p.mockProvider.AddMagnet(ctx, magnet)
}

func (p *sourceTestProvider) AddTorrentFile(ctx context.Context, data []byte, filename string) (*AddMagnetResult, error) {
	p.uploads++
	p.data, p.filename = data, filename
	return p.mockProvider.AddTorrentFile(ctx, data, filename)
}

func TestIndexerMagnetRedirectAcrossProviderPaths(t *testing.T) {
	srv := newMagnetRedirectServer(t)
	for _, mode := range []string{"multi-provider", "single-provider", "health", "batch"} {
		t.Run(mode, func(t *testing.T) {
			p := &sourceTestProvider{mockProvider: &mockProvider{
				name:            "redirect_" + mode,
				status:          "downloaded",
				torrentFilename: "Big.Brother.US.S10E17",
				files:           []File{{ID: 1, Path: "Big.Brother.US.S10E17.mkv", Bytes: 1_000_000, Selected: 1}},
				links:           []string{"https://download.example.com/episode.mkv"},
			}}
			candidate := models.NZBResult{
				Title: "Big.Brother.US.S10E17", ServiceType: models.ServiceTypeDebrid,
				Link: srv.URL + "/download", Attributes: map[string]string{"torrentURL": srv.URL + "/download"},
			}
			ctx := context.Background()
			switch mode {
			case "multi-provider":
				result := (&MultiProviderService{}).checkProviderCache(ctx, candidate, providerEntry{
					client: p, config: &config.DebridProviderSettings{Name: p.name},
				})
				if result.Error != nil || !result.IsCached {
					t.Fatalf("cache check: %+v", result)
				}
			case "single-provider":
				result, err := (&PlaybackService{}).resolveWithProvider(ctx, p, candidate, "", candidate.Link)
				if err != nil || result.WebDAVPath == "" {
					t.Fatalf("playback: result=%+v err=%v", result, err)
				}
			case "health":
				// Authoritative uncached status avoids media probing while proving
				// the redirected source reaches the provider successfully.
				p.cacheStatusKnown = true
				result, err := NewHealthService(nil).checkProviderHealth(ctx, p, candidate, "", candidate.Link, true)
				if err != nil || result.Status != "not_cached" {
					t.Fatalf("health: result=%+v err=%v", result, err)
				}
			case "batch":
				svc := newTestPlaybackService(t, p.mockProvider)
				RegisterProvider(p.name, func(string) Provider { return p })
				result, err := svc.ResolveBatch(ctx, candidate, []models.BatchEpisodeTarget{{SeasonNumber: 10, EpisodeNumber: 17}})
				if err != nil || result == nil {
					t.Fatalf("batch: result=%+v err=%v", result, err)
				}
			}
			if p.magnet != redirectMagnet || p.uploads != 0 {
				t.Fatalf("magnet=%q uploads=%d; want original magnet and no file uploads", p.magnet, p.uploads)
			}
		})
	}
}

func TestTorrentPreflightEnrichesMagnetRedirect(t *testing.T) {
	srv := newMagnetRedirectServer(t)
	candidates := []models.NZBResult{{
		Title: "Big.Brother.US.S10E17", ServiceType: models.ServiceTypeDebrid,
		Link: srv.URL + "/download", Attributes: map[string]string{"torrentURL": srv.URL + "/download"},
	}}
	svc := &PlaybackService{preflightData: newTorrentMetainfoCache()}
	if n := svc.enrichTorrentFileGroups(context.Background(), candidates, 1, 1); n != 1 {
		t.Fatalf("enriched groups=%d, want 1", n)
	}
	if candidates[0].Link != redirectMagnet || candidates[0].Attributes["infoHash"] != extractInfoHashFromMagnet(redirectMagnet) {
		t.Fatalf("preflight did not preserve magnet/hash: %+v", candidates[0])
	}
}

func TestDownloadTorrentSourcePreservesTorrentFileUpload(t *testing.T) {
	data := []byte("d4:infod4:name9:movie.mkvee")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/download" {
			http.Redirect(w, r, "/file", http.StatusFound)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="private.torrent"`)
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	source, err := downloadTorrentSource(context.Background(), srv.URL+"/download", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	p := &sourceTestProvider{mockProvider: &mockProvider{}}
	if _, err := source.add(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if p.uploads != 1 || p.magnet != "" || string(p.data) != string(data) || p.filename != "private.torrent" {
		t.Fatalf("torrent upload changed: %+v", p)
	}
}

func TestDownloadTorrentSourceRejectsInvalidRedirectAndLoops(t *testing.T) {
	for _, location := range []string{"magnet:?dn=NoHash", "/loop"} {
		t.Run(location, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, location, http.StatusFound)
			}))
			defer srv.Close()
			if _, err := downloadTorrentSource(context.Background(), srv.URL, time.Second); err == nil {
				t.Fatal("expected invalid source error")
			}
		})
	}
}

func TestIndexerDownloadFailureDoesNotClaimTorrentIsUncached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	providers := []config.DebridProviderSettings{
		{Provider: "redirect_lookup_failure_a", APIKey: "test", Enabled: true},
		{Provider: "redirect_lookup_failure_b", APIKey: "test", Enabled: true},
	}
	for _, provider := range providers {
		mock := &mockProvider{name: provider.Provider}
		RegisterProvider(provider.Provider, func(string) Provider { return mock })
	}
	svc := newTestPlaybackServiceWithProviders(t, providers)
	_, err := svc.multiProvider.CheckCacheAcrossProviders(context.Background(), models.NZBResult{
		Title: "Big.Brother.US.S10E17", ServiceType: models.ServiceTypeDebrid,
		Attributes: map[string]string{"torrentURL": srv.URL},
	})
	if err == nil || !strings.Contains(err.Error(), "torrent cache check failed") || strings.Contains(err.Error(), "not cached") {
		t.Fatalf("expected lookup failure, got %v", err)
	}
}
