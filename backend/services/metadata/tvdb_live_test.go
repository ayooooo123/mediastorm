package metadata

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Opt-in TVDB contract probe: remote id lookup by IMDb id, and translations
// embedded in extended records (meta=translations). Normal tests never contact TVDB.
func TestTVDBLiveRemoteIDAndExtendedTranslations(t *testing.T) {
	key := os.Getenv("STRMR_TVDB_LIVE_API_KEY")
	if key == "" {
		t.Skip("set STRMR_TVDB_LIVE_API_KEY to run live TVDB probe")
	}
	var calls atomic.Int64
	client := &http.Client{Timeout: 15 * time.Second, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api4.thetvdb.com" {
			return nil, fmt.Errorf("unexpected provider host %s", r.URL.Host)
		}
		calls.Add(1)
		return http.DefaultTransport.RoundTrip(r)
	})}
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{client: newTVDBClient(key, "eng", client, 24), cache: cache, idCache: newFileCache(t.TempDir(), 168), inflightRequests: make(map[string]*inflightRequest)}

	series, ok := svc.searchTVDBByIMDBID("tt0903747", "series") // Breaking Bad
	if !ok || len(series) == 0 || series[0].TVDBID != "81189" {
		t.Fatalf("series remote id: ok=%v results=%+v", ok, series)
	}
	movie, ok := svc.searchTVDBByIMDBID("tt0133093", "movie") // The Matrix
	if !ok || len(movie) == 0 || movie[0].TVDBID == "" {
		t.Fatalf("movie remote id: ok=%v results=%+v", ok, movie)
	}
	if none, ok := svc.searchTVDBByIMDBID("tt0133093", "series"); ok || len(none) > 0 {
		t.Fatalf("movie id matched a series: %+v", none)
	}
	t.Logf("remote id: series=%s movie=%s %q", series[0].TVDBID, movie[0].TVDBID, movie[0].Name)

	seriesExt, err := svc.cachedSeriesExtended(81189, nil)
	if err != nil {
		t.Fatalf("series extended: %v", err)
	}
	tr, embedded, found := seriesExt.translation("fra")
	t.Logf("series extended: name=%q artworks=%d nameLangs=%d embeddedNames=%d embeddedOverviews=%d fra=%q found=%v embedded=%v",
		seriesExt.Name, len(seriesExt.Artworks), len(seriesExt.NameLanguages), len(seriesExt.Translations.NameTranslations),
		len(seriesExt.Translations.OverviewTranslations), tr.Name, found, embedded)
	if !embedded || !found || strings.TrimSpace(tr.Overview) == "" {
		t.Fatalf("series extended carries no embedded French translation (embedded=%v found=%v)", embedded, found)
	}
	if len(seriesExt.Artworks) == 0 {
		t.Fatal("series extended has no artworks without meta=artworks")
	}

	movieID := int64(0)
	fmt.Sscan(movie[0].TVDBID, &movieID)
	movieExt, err := svc.cachedMovieExtended(movieID, nil)
	if err != nil {
		t.Fatalf("movie extended: %v", err)
	}
	mtr, membedded, mfound := movieExt.translation("deu")
	t.Logf("movie extended: name=%q artworks=%d embedded=%v deu=%q found=%v", movieExt.Name, len(movieExt.Artworks), membedded, mtr.Name, mfound)
	if !membedded || !mfound {
		t.Fatalf("movie extended carries no embedded German translation (embedded=%v found=%v)", membedded, mfound)
	}

	episodes, err := svc.cachedSeriesExtended(81189, []string{"episodes"})
	if err != nil || len(episodes.Episodes) == 0 {
		t.Fatalf("series episodes variant: err=%v episodes=%d", err, len(episodes.Episodes))
	}
	before := calls.Load()
	if _, err := svc.cachedSeriesExtended(81189, []string{"artworks"}); err != nil {
		t.Fatalf("series extended (artworks meta): %v", err)
	}
	if calls.Load() != before {
		t.Fatal("legacy artworks meta refetched instead of sharing the cached record")
	}
	t.Logf("episodes=%d total TVDB calls=%d", len(episodes.Episodes), calls.Load())
}
