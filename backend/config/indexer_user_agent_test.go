package config

import "testing"

func TestIndexerUserAgentMatchesByName(t *testing.T) {
	s := Settings{Indexers: []IndexerConfig{
		{Name: "NZBGeek"},
		{Name: "SceneIndexer", UserAgent: " Sonarr/4.0.20.3014 (ubuntu 24.04) "},
	}}
	if got := s.IndexerUserAgent("sceneindexer"); got != "Sonarr/4.0.20.3014 (ubuntu 24.04)" {
		t.Fatalf("got %q", got)
	}
	if got := s.IndexerUserAgent("NZBGeek"); got != "" {
		t.Fatalf("expected no override, got %q", got)
	}
	if got := s.IndexerUserAgent(""); got != "" {
		t.Fatalf("expected empty for blank name, got %q", got)
	}
}
