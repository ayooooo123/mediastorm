package debrid

import "testing"

func TestAnnotateDirectStreamCacheHint(t *testing.T) {
	tests := []struct {
		name       string
		attributes map[string]string
		want       string
	}{
		{
			name: "comet cached",
			attributes: map[string]string{
				"scraper": "comet", "preresolved": "true", "label": "[RD⚡] Comet 2160p",
			},
			want: directCacheLikelyCached,
		},
		{
			name: "comet uncached",
			attributes: map[string]string{
				"scraper": "comet", "preresolved": "true", "label": "[RD⬇️] Comet 2160p",
			},
			want: directCacheLikelyUncached,
		},
		{
			name: "mediafusion cached variation selector",
			attributes: map[string]string{
				"scraper": "mediafusion", "preresolved": "true", "label": "MediaFusion | ElfHosted 🧲 RD ⚡️ 1080p",
			},
			want: directCacheLikelyCached,
		},
		{
			name: "aiostreams raw name",
			attributes: map[string]string{
				"scraper": "aiostreams", "preresolved": "true", "raw_name": "RD ⚡ AIOStreams 4K",
			},
			want: directCacheLikelyCached,
		},
		{
			name: "ordinary direct stream has no hint",
			attributes: map[string]string{
				"scraper": "aiostreams", "preresolved": "true", "raw_name": "AIOStreams 1080p",
			},
		},
		{
			name: "internet archive is not a cache hint provider",
			attributes: map[string]string{
				"scraper": "internetarchive", "preresolved": "true", "label": "⚡ archive file",
			},
		},
		{
			name: "torrent result is ignored",
			attributes: map[string]string{
				"scraper": "comet", "label": "[RD⚡] Comet 2160p",
			},
		},
		{
			name: "ambiguous markers are ignored",
			attributes: map[string]string{
				"scraper": "comet", "preresolved": "true", "label": "RD ⚡ ⬇",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotateDirectStreamCacheHint(tt.attributes)
			if got := tt.attributes[directCacheStatusAttribute]; got != tt.want {
				t.Fatalf("source cache status = %q, want %q", got, tt.want)
			}
			if tt.want != "" && tt.attributes[directCacheEvidenceAttribute] != "provider_label" {
				t.Fatalf("source cache evidence = %q, want provider_label", tt.attributes[directCacheEvidenceAttribute])
			}
		})
	}
}

func TestNormalizeScrapeResultAddsDirectCacheHint(t *testing.T) {
	result := normalizeScrapeResult(ScrapeResult{
		Title:      "Movie.2026.2160p.mkv",
		Indexer:    "Comet",
		TorrentURL: "https://stream.example.test/movie.mkv",
		Attributes: map[string]string{
			"scraper":     "comet",
			"preresolved": "true",
			"label":       "[RD⚡] Comet 2160p",
		},
	})

	if got := result.Attributes[directCacheStatusAttribute]; got != directCacheLikelyCached {
		t.Fatalf("source cache status = %q, want %q", got, directCacheLikelyCached)
	}
	if got := result.Attributes[directCacheEvidenceAttribute]; got != "provider_label" {
		t.Fatalf("source cache evidence = %q, want provider_label", got)
	}
}
