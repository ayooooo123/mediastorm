package metadata

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	xdraw "golang.org/x/image/draw"
)

type failingRoundTripper struct {
	calls int
}

func (rt *failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	rt.calls++
	return nil, errors.New("temporary network failure")
}

func TestDoGETStopsRetryBackoffWhenContextCanceled(t *testing.T) {
	rt := &failingRoundTripper{}
	c := newTMDBClient("test-key", "en", &http.Client{Transport: rt}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	started := time.Now()
	err := c.doGET(ctx, "https://api.themoviedb.org/test", &struct{}{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("doGET error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("doGET took %v after cancellation, want under 200ms", elapsed)
	}
	if rt.calls != 1 {
		t.Fatalf("network calls = %d, want 1", rt.calls)
	}
}

// countingRoundTripper returns a canned response for every request and counts calls.
type countingRoundTripper struct {
	mu     sync.Mutex
	calls  int
	body   string
	status int
}

func (rt *countingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.calls++
	rt.mu.Unlock()
	status := rt.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(rt.body)),
		Header:     make(http.Header),
	}, nil
}

func (rt *countingRoundTripper) callCount() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.calls
}

// TestMovieDetails_FileCacheAndSingleflightCleanup verifies that movieDetails
// persists results to the file cache (so repeat calls don't re-hit TMDB) and
// that the in-flight singleflight map does not retain completed entries.
func TestMovieDetails_FileCacheAndSingleflightCleanup(t *testing.T) {
	rt := &countingRoundTripper{body: `{"id":603,"title":"The Matrix","release_date":"1999-03-30","imdb_id":"tt0133093","runtime":136}`}
	cache := newFileCache(t.TempDir(), 24)
	c := newTMDBClient("test-key", "en", &http.Client{Transport: rt}, cache)

	ctx := context.Background()

	got, err := c.movieDetails(ctx, 603)
	if err != nil {
		t.Fatalf("first movieDetails: unexpected error: %v", err)
	}
	if got == nil || got.Name != "The Matrix" {
		t.Fatalf("first movieDetails: got %+v, want name=The Matrix", got)
	}
	if rt.callCount() != 1 {
		t.Fatalf("expected 1 network call after first fetch, got %d", rt.callCount())
	}

	// The singleflight map must not retain entries once a fetch completes.
	leaked := 0
	c.movieCache.Range(func(_, _ any) bool { leaked++; return true })
	if leaked != 0 {
		t.Fatalf("movieCache leaked %d entries after fetch, want 0", leaked)
	}

	// Second call must be served from the file cache — no additional network call.
	got2, err := c.movieDetails(ctx, 603)
	if err != nil {
		t.Fatalf("second movieDetails: unexpected error: %v", err)
	}
	if got2 == nil || got2.Name != "The Matrix" {
		t.Fatalf("second movieDetails: got %+v, want name=The Matrix", got2)
	}
	if rt.callCount() != 1 {
		t.Fatalf("expected file-cache hit (still 1 call), got %d", rt.callCount())
	}
}

func TestNormalizeLanguage(t *testing.T) {
	tests := map[string]string{
		"":      "en-US",
		"en":    "en-US",
		"en_US": "en-US",
		"pt-br": "pt-BR",
		"fr-FR": "fr-FR",
		"es":    "es-US",
	}
	for input, expect := range tests {
		if got := normalizeLanguage(input); got != expect {
			t.Fatalf("normalizeLanguage(%q) = %q, want %q", input, got, expect)
		}
	}
}

func TestBuildTMDBImage(t *testing.T) {
	if img := buildTMDBImage("", tmdbPosterSize, "poster"); img != nil {
		t.Fatal("expected nil image when path empty")
	}
	img := buildTMDBImage("/poster.png", tmdbPosterSize, "poster")
	if img == nil {
		t.Fatal("expected image for valid path")
	}
	if img.URL != "https://image.tmdb.org/t/p/w780/poster.png" {
		t.Fatalf("unexpected image url: %s", img.URL)
	}
	if img.Type != "poster" {
		t.Fatalf("unexpected image type: %s", img.Type)
	}
}

func TestParseTMDBYear(t *testing.T) {
	if year := parseTMDBYear("2024-05-01", ""); year != 2024 {
		t.Fatalf("expected 2024, got %d", year)
	}
	if year := parseTMDBYear("", "2019-01-01"); year != 2019 {
		t.Fatalf("expected 2019, got %d", year)
	}
	if year := parseTMDBYear("199", ""); year != 0 {
		t.Fatalf("expected 0 for invalid date, got %d", year)
	}
}

func TestBackdropVisualDiversityScore(t *testing.T) {
	makeImage := func(left, right color.Color) image.Image {
		img := image.NewRGBA(image.Rect(0, 0, 160, 90))
		for y := 0; y < 90; y++ {
			for x := 0; x < 160; x++ {
				if x < 80 {
					img.Set(x, y, left)
				} else {
					img.Set(x, y, right)
				}
			}
		}
		return img
	}

	primary := computeBackdropVisualSignature(makeImage(color.NRGBA{R: 220, G: 40, B: 40, A: 255}, color.NRGBA{R: 30, G: 40, B: 220, A: 255}))
	similar := computeBackdropVisualSignature(makeImage(color.NRGBA{R: 210, G: 45, B: 45, A: 255}, color.NRGBA{R: 35, G: 45, B: 210, A: 255}))
	different := computeBackdropVisualSignature(makeImage(color.NRGBA{R: 20, G: 180, B: 80, A: 255}, color.NRGBA{R: 230, G: 220, B: 40, A: 255}))

	similarScore := backdropVisualDiversityScore(primary, similar)
	differentScore := backdropVisualDiversityScore(primary, different)
	if differentScore <= similarScore {
		t.Fatalf("different score = %f, similar score = %f; want different higher", differentScore, similarScore)
	}
	if similarScore >= 0 {
		t.Fatalf("similar score = %f, want duplicate-like candidate penalized below zero", similarScore)
	}

	keyArt := image.NewRGBA(image.Rect(0, 0, 220, 124))
	for y := 0; y < 124; y++ {
		for x := 0; x < 220; x++ {
			switch {
			case x < 80:
				keyArt.Set(x, y, color.NRGBA{R: 190, G: 20, B: 30, A: 255})
			case y < 52:
				keyArt.Set(x, y, color.NRGBA{R: 20, G: 20, B: 30, A: 255})
			case x > 150:
				keyArt.Set(x, y, color.NRGBA{R: 20, G: 70, B: 190, A: 255})
			default:
				keyArt.Set(x, y, color.NRGBA{R: 230, G: 180, B: 60, A: 255})
			}
		}
	}
	croppedKeyArt := image.NewRGBA(image.Rect(0, 0, 220, 124))
	xdraw.ApproxBiLinear.Scale(croppedKeyArt, croppedKeyArt.Bounds(), keyArt, image.Rect(0, 12, 185, 124), xdraw.Over, nil)

	primaryKeyArt := computeBackdropVisualSignature(keyArt)
	croppedKeyArtSig := computeBackdropVisualSignature(croppedKeyArt)
	if cropAlignedDuplicateScore(primaryKeyArt, croppedKeyArtSig) < 28 {
		t.Fatalf("crop-aligned duplicate score = %f, want duplicate crop above threshold", cropAlignedDuplicateScore(primaryKeyArt, croppedKeyArtSig))
	}
	if score := backdropVisualDiversityScore(primaryKeyArt, croppedKeyArtSig); score >= 0 {
		t.Fatalf("cropped key-art score = %f, want duplicate-like candidate penalized below zero", score)
	}
}

func TestIsImageDark(t *testing.T) {
	// Helper to create a PNG with a given color and optional transparent pixels
	makePNG := func(c color.Color, transparent bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			img := image.NewNRGBA(image.Rect(0, 0, 10, 10))
			for y := 0; y < 10; y++ {
				for x := 0; x < 10; x++ {
					if transparent && x < 5 {
						img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 0})
					} else {
						r, g, b, a := c.RGBA()
						img.SetNRGBA(x, y, color.NRGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)})
					}
				}
			}
			w.Header().Set("Content-Type", "image/png")
			png.Encode(w, img)
		}
	}

	tests := []struct {
		name     string
		handler  http.HandlerFunc
		wantDark bool
	}{
		{
			name:     "solid black image not marked dark (solid background)",
			handler:  makePNG(color.Black, false),
			wantDark: false, // >70% opaque = solid background, skip dark flag
		},
		{
			name:     "white opaque logo is not dark",
			handler:  makePNG(color.White, false),
			wantDark: false,
		},
		{
			name:     "black on transparent background is dark",
			handler:  makePNG(color.Black, true),
			wantDark: true, // 50% opaque = cutout logo, dark flag applies
		},
		{
			name:     "solid dark gray not marked dark (solid background)",
			handler:  makePNG(color.NRGBA{30, 30, 30, 255}, false),
			wantDark: false, // >70% opaque = solid background
		},
		{
			name:     "mid gray (luminance ~128) is not dark",
			handler:  makePNG(color.NRGBA{128, 128, 128, 255}, false),
			wantDark: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			client := &tmdbClient{
				httpc: srv.Client(),
			}
			// isImageDark replaces /w500/ with /w92/, so include that in the URL
			testURL := srv.URL + "/w500/test.png"
			got := client.isImageDark(context.Background(), testURL)
			if got != tc.wantDark {
				t.Errorf("isImageDark() = %v, want %v", got, tc.wantDark)
			}
		})
	}
}

func TestIsImageDark_FetchError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	client := &tmdbClient{
		httpc: srv.Client(),
	}
	// Should return false on error (safe default)
	got := client.isImageDark(context.Background(), srv.URL+"/w500/missing.png")
	if got {
		t.Error("expected false on fetch error")
	}
}

func TestFetchImages_LogoLanguagePreference(t *testing.T) {
	selectLogo := func(logos []tmdbImageItem, preferredLang string) string {
		selected, ok := selectLogoCandidate(logos, preferredLang, func(tmdbImageItem) bool { return false })
		if !ok {
			return ""
		}
		return selected.FilePath
	}

	tests := []struct {
		name          string
		logos         []tmdbImageItem
		preferredLang string
		wantPath      string
		description   string
	}{
		{
			name: "english user: english preferred over portuguese",
			logos: []tmdbImageItem{
				{FilePath: "/pt_logo.png", ISO6391: "pt", VoteAverage: 9.0},
				{FilePath: "/en_logo.png", ISO6391: "en", VoteAverage: 5.0},
			},
			preferredLang: "en",
			wantPath:      "/en_logo.png",
			description:   "English logo should be selected even with lower vote average",
		},
		{
			name: "english user: english preferred over no-language",
			logos: []tmdbImageItem{
				{FilePath: "/null_logo.png", ISO6391: "", VoteAverage: 9.0},
				{FilePath: "/en_logo.png", ISO6391: "en", VoteAverage: 5.0},
			},
			preferredLang: "en",
			wantPath:      "/en_logo.png",
			description:   "English logo should beat no-language logo",
		},
		{
			name: "english user: no-language selected when mixed with foreign",
			logos: []tmdbImageItem{
				{FilePath: "/pt_logo.png", ISO6391: "pt", VoteAverage: 9.0},
				{FilePath: "/null_logo.png", ISO6391: "", VoteAverage: 3.0},
			},
			preferredLang: "en",
			wantPath:      "/null_logo.png",
			description:   "No-language logo should be selected; foreign logos filtered out",
		},
		{
			name: "english user: only foreign logos returns nil (Lucas the Spider case)",
			logos: []tmdbImageItem{
				{FilePath: "/pt_logo.png", ISO6391: "pt", VoteAverage: 5.0},
				{FilePath: "/es_logo.png", ISO6391: "es", VoteAverage: 3.0},
			},
			preferredLang: "en",
			wantPath:      "",
			description:   "Foreign-only logos should be skipped for English users",
		},
		{
			name: "portuguese user: portuguese preferred over english",
			logos: []tmdbImageItem{
				{FilePath: "/en_logo.png", ISO6391: "en", VoteAverage: 9.0},
				{FilePath: "/pt_logo.png", ISO6391: "pt", VoteAverage: 5.0},
			},
			preferredLang: "pt",
			wantPath:      "/pt_logo.png",
			description:   "User's preferred language should win over English",
		},
		{
			name: "portuguese user: falls back to english when no pt logo",
			logos: []tmdbImageItem{
				{FilePath: "/en_logo.png", ISO6391: "en", VoteAverage: 5.0},
				{FilePath: "/fr_logo.png", ISO6391: "fr", VoteAverage: 9.0},
			},
			preferredLang: "pt",
			wantPath:      "/en_logo.png",
			description:   "Should fall back to English when preferred language unavailable",
		},
		{
			name: "portuguese user: preferred lang over no-language",
			logos: []tmdbImageItem{
				{FilePath: "/null_logo.png", ISO6391: "", VoteAverage: 9.0},
				{FilePath: "/pt_logo.png", ISO6391: "pt", VoteAverage: 3.0},
			},
			preferredLang: "pt",
			wantPath:      "/pt_logo.png",
			description:   "User's language should win over no-language",
		},
		{
			name: "highest voted english logo wins among english",
			logos: []tmdbImageItem{
				{FilePath: "/en_low.png", ISO6391: "en", VoteAverage: 2.0},
				{FilePath: "/en_high.png", ISO6391: "en", VoteAverage: 8.0},
			},
			preferredLang: "en",
			wantPath:      "/en_high.png",
			description:   "Among english logos, highest vote average should win",
		},
		{
			name:          "no logos returns nil",
			logos:         []tmdbImageItem{},
			preferredLang: "en",
			wantPath:      "",
			description:   "Empty logo list should return nil logo",
		},
		{
			name: "all tiers present picks user's language",
			logos: []tmdbImageItem{
				{FilePath: "/en_logo.png", ISO6391: "en", VoteAverage: 10.0},
				{FilePath: "/null_logo.png", ISO6391: "", VoteAverage: 8.0},
				{FilePath: "/fr_logo.png", ISO6391: "fr", VoteAverage: 3.0},
			},
			preferredLang: "fr",
			wantPath:      "/fr_logo.png",
			description:   "User's language should win even with lowest vote average",
		},
		{
			name: "single foreign logo returns nil for english user",
			logos: []tmdbImageItem{
				{FilePath: "/ja_logo.png", ISO6391: "ja", VoteAverage: 8.0},
			},
			preferredLang: "en",
			wantPath:      "",
			description:   "A non-English/non-preferred logo should be skipped",
		},
		{
			name: "no-language fallback when no preferred or english",
			logos: []tmdbImageItem{
				{FilePath: "/null_logo.png", ISO6391: "", VoteAverage: 5.0},
				{FilePath: "/ja_logo.png", ISO6391: "ja", VoteAverage: 9.0},
			},
			preferredLang: "fr",
			wantPath:      "/null_logo.png",
			description:   "No-language logo used as last resort",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := selectLogo(tc.logos, tc.preferredLang)
			if got != tc.wantPath {
				t.Errorf("selected %q, want %q\n  %s", got, tc.wantPath, tc.description)
			}
		})
	}
}

func TestSelectLogoCandidate_SkipsWhiteOnlySVGWithinSameLanguageTier(t *testing.T) {
	logos := []tmdbImageItem{
		{FilePath: "/white.svg", ISO6391: "en", VoteAverage: 9.0},
		{FilePath: "/colored.png", ISO6391: "en", VoteAverage: 5.0},
		{FilePath: "/fallback.png", ISO6391: "", VoteAverage: 10.0},
	}

	selected, ok := selectLogoCandidate(logos, "en", func(item tmdbImageItem) bool {
		return item.FilePath == "/white.svg"
	})
	if !ok {
		t.Fatal("expected a selected logo")
	}
	if selected.FilePath != "/colored.png" {
		t.Fatalf("selected %q, want colored same-language logo", selected.FilePath)
	}
}

func TestSelectLogoCandidate_KeepsWhiteOnlySVGWhenOnlyLowerLanguageTierAlternativesExist(t *testing.T) {
	logos := []tmdbImageItem{
		{FilePath: "/white.svg", ISO6391: "en", VoteAverage: 9.0},
		{FilePath: "/fallback.png", ISO6391: "", VoteAverage: 10.0},
	}

	selected, ok := selectLogoCandidate(logos, "en", func(item tmdbImageItem) bool {
		return item.FilePath == "/white.svg"
	})
	if !ok {
		t.Fatal("expected a selected logo")
	}
	if selected.FilePath != "/white.svg" {
		t.Fatalf("selected %q, want preferred-language logo", selected.FilePath)
	}
}

func TestIsWhiteOnlySVGXML(t *testing.T) {
	tests := []struct {
		name string
		svg  string
		want bool
	}{
		{
			name: "white class fill",
			svg:  `<svg><style>.st0{fill:#FFFFFF;}</style><path class="st0"/></svg>`,
			want: true,
		},
		{
			name: "colored class fill",
			svg:  `<svg><style>.st0{fill:#FEE303;}.st1{fill:#FFFFFF;}</style><path class="st0"/><path class="st1"/></svg>`,
			want: false,
		},
		{
			name: "rgb white fill",
			svg:  `<svg><path fill="rgb(255,255,255)"/></svg>`,
			want: true,
		},
		{
			name: "no fill",
			svg:  `<svg><path d="M0 0"/></svg>`,
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWhiteOnlySVGXML(tc.svg); got != tc.want {
				t.Fatalf("isWhiteOnlySVGXML() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLogoLanguage(t *testing.T) {
	tests := []struct {
		language string
		want     string
	}{
		{"", "en"},
		{"en", "en"},
		{"eng", "en"},
		{"en-US", "en"},
		{"pt-BR", "pt"},
		{"pt_BR", "pt"},
		{"por", "pt"},
		{"fr", "fr"},
		{"fra", "fr"},
		{"ja", "ja"},
		{"jpn", "ja"},
	}
	for _, tc := range tests {
		t.Run(tc.language, func(t *testing.T) {
			client := &tmdbClient{language: tc.language}
			got := client.logoLanguage()
			if got != tc.want {
				t.Errorf("logoLanguage(%q) = %q, want %q", tc.language, got, tc.want)
			}
		})
	}
}

func TestFetchShelfTitlesSupportsEveryTMDBSourceType(t *testing.T) {
	tests := []struct {
		name          string
		opts          TMDBListOptions
		wantPath      string
		wantMediaType string
		response      string
		checkQuery    func(*testing.T, *http.Request)
	}{
		{
			name:          "public list",
			opts:          TMDBListOptions{SourceType: TMDBSourcePublicList, SourceID: "10", MediaType: "movie", Sort: "original"},
			wantPath:      "/3/list/10",
			wantMediaType: "movie",
			response:      `{"items":[{"id":101,"title":"Public Movie","media_type":"movie","poster_path":"/public.jpg","release_date":"2024-01-01","original_language":"en","genre_ids":[28],"vote_average":8,"vote_count":500}]}`,
		},
		{
			name:          "production company",
			opts:          TMDBListOptions{SourceType: TMDBSourceProductionCompany, SourceID: "10", MediaType: "movie", Sort: "popularity.desc"},
			wantPath:      "/3/discover/movie",
			wantMediaType: "movie",
			response:      `{"results":[{"id":102,"title":"Company Movie","poster_path":"/company.jpg","release_date":"2024-01-01","original_language":"en","genre_ids":[28],"vote_average":8,"vote_count":500,"popularity":100}],"total_results":1}`,
			checkQuery: func(t *testing.T, req *http.Request) {
				if got := req.URL.Query().Get("with_companies"); got != "10" {
					t.Fatalf("with_companies = %q, want 10", got)
				}
			},
		},
		{
			name:          "production company with documentaries",
			opts:          TMDBListOptions{SourceType: TMDBSourceProductionCompany, SourceID: "10", MediaType: "movie", Sort: "popularity.desc"},
			wantPath:      "/3/discover/movie",
			wantMediaType: "movie",
			response:      `{"results":[{"id":109,"title":"Company Documentary","poster_path":"/documentary.jpg","release_date":"2024-01-01","original_language":"en","genre_ids":[99],"vote_average":8,"vote_count":500,"popularity":100}],"total_results":1}`,
			checkQuery: func(t *testing.T, req *http.Request) {
				if got := req.URL.Query().Get("with_companies"); got != "10" {
					t.Fatalf("with_companies = %q, want 10", got)
				}
				if got := req.URL.Query().Get("without_genres"); got != "" {
					t.Fatalf("without_genres = %q, want empty", got)
				}
				if got := req.URL.Query().Get("with_runtime.gte"); got != "" {
					t.Fatalf("with_runtime.gte = %q, want empty", got)
				}
			},
		},
		{
			name:          "production company series",
			opts:          TMDBListOptions{SourceType: TMDBSourceProductionCompany, SourceID: "10", MediaType: "tv", Sort: "popularity.desc"},
			wantPath:      "/3/discover/tv",
			wantMediaType: "series",
			response:      `{"results":[{"id":110,"name":"Company Series","poster_path":"/series.jpg","first_air_date":"2024-01-01","original_language":"en","genre_ids":[28],"vote_average":8,"vote_count":500,"popularity":100}],"total_results":1}`,
		},
		{
			name:          "network",
			opts:          TMDBListOptions{SourceType: TMDBSourceNetwork, SourceID: "213", MediaType: "movie", Sort: "popularity.desc"},
			wantPath:      "/3/discover/tv",
			wantMediaType: "series",
			response:      `{"results":[{"id":103,"name":"Network Series","poster_path":"/network.jpg","first_air_date":"2024-01-01","original_language":"en","genre_ids":[28],"vote_average":8,"vote_count":500,"popularity":100}],"total_results":1}`,
			checkQuery: func(t *testing.T, req *http.Request) {
				if got := req.URL.Query().Get("with_networks"); got != "213" {
					t.Fatalf("with_networks = %q, want 213", got)
				}
			},
		},
		{
			name:          "movie collection",
			opts:          TMDBListOptions{SourceType: TMDBSourceMovieCollection, SourceID: "10", MediaType: "tv", Sort: "original"},
			wantPath:      "/3/collection/10",
			wantMediaType: "movie",
			response:      `{"parts":[{"id":104,"title":"Collection Movie","poster_path":"/collection.jpg","release_date":"2024-01-01","original_language":"en","genre_ids":[28],"vote_average":8,"vote_count":500}]}`,
		},
		{
			name:          "person credits",
			opts:          TMDBListOptions{SourceType: TMDBSourcePersonCredits, SourceID: "31", MediaType: "movie", Sort: "popularity.desc"},
			wantPath:      "/3/person/31/combined_credits",
			wantMediaType: "movie",
			response:      `{"cast":[{"id":105,"title":"Actor Movie","media_type":"movie","poster_path":"/actor.jpg","release_date":"2024-01-01","original_language":"en","genre_ids":[28],"vote_average":8,"vote_count":500,"popularity":100}]}`,
		},
		{
			name:          "director credits",
			opts:          TMDBListOptions{SourceType: TMDBSourceDirectorCredits, SourceID: "31", MediaType: "movie", Sort: "popularity.desc"},
			wantPath:      "/3/person/31/combined_credits",
			wantMediaType: "movie",
			response:      `{"crew":[{"id":106,"title":"Directed Movie","media_type":"movie","poster_path":"/director.jpg","job":"Director","release_date":"2024-01-01","original_language":"en","genre_ids":[28],"vote_average":8,"vote_count":500,"popularity":100},{"id":999,"title":"Produced Movie","media_type":"movie","job":"Producer"}]}`,
		},
		{
			name:          "custom discover",
			opts:          TMDBListOptions{SourceType: TMDBSourceCustomDiscover, MediaType: "movie", Sort: "vote_average.desc"},
			wantPath:      "/3/discover/movie",
			wantMediaType: "movie",
			response:      `{"results":[{"id":107,"title":"Discovered Movie","poster_path":"/discover.jpg","release_date":"2024-01-01","original_language":"en","genre_ids":[28],"vote_average":8,"vote_count":500,"popularity":100}],"total_results":1}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.opts.Limit = 10
			test.opts.DiscoverQuery = "genres=28&date.gte=2024-01-01&date.lte=2024-12-31&rating.gte=7&rating.lte=10&votes.gte=100&language=en&year=2024"
			client := newTMDBClient("test-key", "en", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != test.wantPath {
					t.Fatalf("path = %q, want %q", req.URL.Path, test.wantPath)
				}
				if test.checkQuery != nil {
					test.checkQuery(t, req)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(test.response)),
				}, nil
			})}, nil)

			titles, total, err := client.fetchShelfTitles(context.Background(), test.opts)
			if err != nil {
				t.Fatalf("fetchShelfTitles: %v", err)
			}
			if total != 1 || len(titles) != 1 {
				t.Fatalf("total=%d titles=%d, want 1/1", total, len(titles))
			}
			if titles[0].MediaType != test.wantMediaType {
				t.Fatalf("mediaType=%q, want %q", titles[0].MediaType, test.wantMediaType)
			}
			if titles[0].TMDBID == 999 {
				t.Fatal("director credits included a non-director crew item")
			}
		})
	}
}

func TestSearchShelfSourcesSupportsNamesAndURLs(t *testing.T) {
	t.Run("company name", func(t *testing.T) {
		client := newTMDBClient("test-key", "en", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/3/search/company" || req.URL.Query().Get("query") != "Marvel Studios" {
				t.Fatalf("unexpected request %s", req.URL.String())
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"results":[{"id":420,"name":"Marvel Studios"}]}`))}, nil
		})}, nil)
		results, err := client.searchShelfSources(context.Background(), TMDBSourceProductionCompany, "Marvel Studios")
		if err != nil || len(results) != 1 || results[0].ID != "420" {
			t.Fatalf("results=%+v err=%v", results, err)
		}
	})

	t.Run("network URL", func(t *testing.T) {
		client := newTMDBClient("test-key", "en", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/3/network/213" {
				t.Fatalf("path=%q, want /3/network/213", req.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":213,"name":"Netflix"}`))}, nil
		})}, nil)
		results, err := client.searchShelfSources(context.Background(), TMDBSourceNetwork, "https://www.themoviedb.org/network/213")
		if err != nil || len(results) != 1 || results[0].Name != "Netflix" {
			t.Fatalf("results=%+v err=%v", results, err)
		}
	})
}

func TestFetchShelfTitlesZeroLimitReturnsEveryPage(t *testing.T) {
	calls := 0
	client := newTMDBClient("test-key", "en", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		page := req.URL.Query().Get("page")
		start, end := 1, 20
		if page == "2" {
			start, end = 21, 25
		}
		rows := make([]string, 0, end-start+1)
		for id := start; id <= end; id++ {
			rows = append(rows, fmt.Sprintf(
				`{"id":%d,"title":"Movie %d","poster_path":"/poster-%d.jpg","release_date":"2024-01-01","original_language":"en","genre_ids":[28],"vote_average":8,"vote_count":500}`,
				id, id, id,
			))
		}
		payload := `{"results":[` + strings.Join(rows, ",") + `],"total_results":25}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(payload)),
		}, nil
	})}, nil)

	titles, total, err := client.fetchShelfTitles(context.Background(), TMDBListOptions{
		SourceType: TMDBSourceCustomDiscover,
		MediaType:  "movie",
		Sort:       "popularity.desc",
		Limit:      0,
	})
	if err != nil {
		t.Fatalf("fetchShelfTitles: %v", err)
	}
	if total != 25 || len(titles) != 25 {
		t.Fatalf("total=%d titles=%d, want 25/25", total, len(titles))
	}
	if calls != 2 {
		t.Fatalf("TMDB page calls=%d, want 2", calls)
	}
}

func TestDiscoverShelfPageUsesPageCache(t *testing.T) {
	var calls atomic.Int32
	client := newTMDBClient(
		"test-key",
		"en",
		&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"results":[{"id":1,"title":"Cached Movie","release_date":"2024-01-01"}],"total_results":1}`,
				)),
			}, nil
		})},
		newFileCache(t.TempDir(), 24),
	)

	for range 2 {
		items, total, err := client.discoverShelfPage(context.Background(), "movie", nil, 1, "popularity.desc")
		if err != nil || total != 1 || len(items) != 1 {
			t.Fatalf("items=%+v total=%d err=%v", items, total, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("TMDB calls=%d, want 1 cached call", got)
	}
}

func TestFetchShelfTitlesFetchesRemainingPagesConcurrently(t *testing.T) {
	var calls atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32
	client := newTMDBClient("test-key", "en", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for {
			previous := maxActive.Load()
			if current <= previous || maxActive.CompareAndSwap(previous, current) {
				break
			}
		}
		page, _ := strconv.Atoi(req.URL.Query().Get("page"))
		if page > 1 {
			time.Sleep(25 * time.Millisecond)
		}
		start := (page-1)*20 + 1
		rows := make([]string, 0, 20)
		for id := start; id < start+20; id++ {
			rows = append(rows, fmt.Sprintf(
				`{"id":%d,"title":"Movie %d","poster_path":"/poster-%d.jpg","release_date":"2024-01-01","popularity":%d}`,
				id, id, id, 101-id,
			))
		}
		payload := `{"results":[` + strings.Join(rows, ",") + `],"total_results":100}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(payload)),
		}, nil
	})}, nil)
	client.minInterval = 0

	titles, total, err := client.fetchShelfTitles(context.Background(), TMDBListOptions{
		SourceType: TMDBSourceCustomDiscover,
		MediaType:  "movie",
		Sort:       "popularity.desc",
		Limit:      100,
	})
	if err != nil {
		t.Fatalf("fetchShelfTitles: %v", err)
	}
	if total != 100 || len(titles) != 100 {
		t.Fatalf("total=%d titles=%d, want 100/100", total, len(titles))
	}
	if got := calls.Load(); got != 5 {
		t.Fatalf("TMDB page calls=%d, want 5", got)
	}
	if got := maxActive.Load(); got < 2 {
		t.Fatalf("max concurrent TMDB calls=%d, want at least 2", got)
	}
}

func TestFetchShelfTitlesKeepsTitlesWithoutPosters(t *testing.T) {
	var calls atomic.Int32
	client := newTMDBClient("test-key", "en", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		payload := `{"results":[
			{"id":1,"title":"Backdrop Only","backdrop_path":"/backdrop.jpg"},
			{"id":2,"title":"No Artwork"}
		],"total_results":2}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(payload)),
		}, nil
	})}, nil)

	titles, total, err := client.fetchShelfTitles(context.Background(), TMDBListOptions{
		SourceType: TMDBSourceProductionCompany,
		SourceID:   "34",
		MediaType:  "movie",
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("fetchShelfTitles: %v", err)
	}
	if total != 2 || len(titles) != 2 {
		t.Fatalf("total=%d titles=%d, want 2/2", total, len(titles))
	}
	if titles[0].Poster == nil || titles[0].Poster.URL != "https://image.tmdb.org/t/p/w780/backdrop.jpg" {
		t.Fatalf("backdrop fallback poster=%+v", titles[0].Poster)
	}
	if titles[1].Poster != nil {
		t.Fatalf("title without artwork should use the client placeholder, got %+v", titles[1].Poster)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("TMDB page calls=%d, want 1", got)
	}
}

func TestFilterStaticShelfTitlesFetchesDetailsConcurrentlyAndCachesThem(t *testing.T) {
	var calls atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32
	client := newTMDBClient(
		"test-key",
		"en",
		&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			current := active.Add(1)
			defer active.Add(-1)
			for {
				previous := maxActive.Load()
				if current <= previous || maxActive.CompareAndSwap(previous, current) {
					break
				}
			}
			time.Sleep(25 * time.Millisecond)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"production_companies":[{"id":420}],"keywords":{"keywords":[]}}`,
				)),
			}, nil
		})},
		newFileCache(t.TempDir(), 24),
	)
	client.minInterval = 0
	items := make([]tmdbShelfTitle, 12)
	for i := range items {
		items[i] = tmdbShelfTitle{ID: int64(i + 1), Title: fmt.Sprintf("Movie %d", i+1), MediaType: "movie"}
	}
	filters := url.Values{"companies": {"420"}}

	for range 2 {
		filtered, err := client.filterStaticShelfTitles(context.Background(), items, filters)
		if err != nil {
			t.Fatalf("filterStaticShelfTitles: %v", err)
		}
		if len(filtered) != len(items) || filtered[0].ID != 1 || filtered[len(filtered)-1].ID != 12 {
			t.Fatalf("filtered titles lost order: %+v", filtered)
		}
	}
	if got := calls.Load(); got != int32(len(items)) {
		t.Fatalf("TMDB detail calls=%d, want %d after cached second load", got, len(items))
	}
	if got := maxActive.Load(); got < 2 {
		t.Fatalf("max concurrent TMDB detail calls=%d, want at least 2", got)
	}
}
