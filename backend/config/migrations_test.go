package config

import (
	"encoding/json"
	"testing"
)

func TestMigrateGlobalLiveProxyToDefaultSource(t *testing.T) {
	settings := DefaultSettings()
	settings.Live.ProxyURL = " socks5://127.0.0.1:18080 "
	settings.Live.Sources = []LivePlaylistSource{
		{ID: "test", Name: "Test"},
		{ID: "default", Name: "Default"},
		{ID: "other", Name: "Other", ProxyURL: "http://other-proxy:8080"},
	}

	if !MigrateGlobalLiveProxyToDefaultSource(&settings) {
		t.Fatal("migration reported no change")
	}
	if settings.Live.ProxyURL != "" {
		t.Fatalf("Live.ProxyURL = %q, want unset", settings.Live.ProxyURL)
	}
	if settings.Live.Sources[0].ProxyURL != "" {
		t.Fatalf("non-default source proxy = %q, want unchanged", settings.Live.Sources[0].ProxyURL)
	}
	if settings.Live.Sources[1].ProxyURL != "socks5://127.0.0.1:18080" {
		t.Fatalf("default source proxy = %q, want migrated proxy", settings.Live.Sources[1].ProxyURL)
	}
	if settings.Live.Sources[2].ProxyURL != "http://other-proxy:8080" {
		t.Fatalf("existing source proxy = %q, want unchanged", settings.Live.Sources[2].ProxyURL)
	}
	if MigrateGlobalLiveProxyToDefaultSource(&settings) {
		t.Fatal("second migration reported a change")
	}
}

func TestMigratePrewarmContinueWatchingOnly(t *testing.T) {
	raw := map[string]interface{}{
		"scheduledTasks": map[string]interface{}{
			"tasks": []interface{}{
				map[string]interface{}{
					"type": "prewarm",
					"config": map[string]interface{}{
						"shelfSelections":     `[{"id":"watchlist","itemScope":"all"}]`,
						"stableReresolveDays": "7",
					},
				},
			},
		},
	}

	MigrateRawSettings(raw)
	tasks := raw["scheduledTasks"].(map[string]interface{})["tasks"].([]interface{})
	configMap := tasks[0].(map[string]interface{})["config"].(map[string]interface{})
	var selections []map[string]interface{}
	if err := json.Unmarshal([]byte(configMap["shelfSelections"].(string)), &selections); err != nil {
		t.Fatalf("decode migrated shelf selections: %v", err)
	}
	if len(selections) != 1 || selections[0]["id"] != "continue-watching" || selections[0]["playedWithinDays"] != float64(14) {
		t.Fatalf("migrated selections = %#v, want Continue Watching/14 days", selections)
	}
	if configMap["stableReresolveDays"] != "7" {
		t.Fatalf("stableReresolveDays = %#v, want preserved", configMap["stableReresolveDays"])
	}
}

func TestMigrateGlobalLiveProxyToDefaultSourcePreservesExistingDefaultProxy(t *testing.T) {
	settings := DefaultSettings()
	settings.Live.ProxyURL = "socks5://global-proxy:1080"
	settings.Live.Sources = []LivePlaylistSource{
		{ID: "default", Name: "Default", ProxyURL: "http://default-proxy:8080"},
	}

	if !MigrateGlobalLiveProxyToDefaultSource(&settings) {
		t.Fatal("migration reported no change")
	}
	if settings.Live.ProxyURL != "" {
		t.Fatalf("Live.ProxyURL = %q, want unset", settings.Live.ProxyURL)
	}
	if settings.Live.Sources[0].ProxyURL != "http://default-proxy:8080" {
		t.Fatalf("default source proxy = %q, want existing value preserved", settings.Live.Sources[0].ProxyURL)
	}
}
