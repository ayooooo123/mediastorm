package config

import (
	"encoding/json"
	"testing"
)

func TestEmbeddedPresetsLoad(t *testing.T) {
	var presets []Preset
	if err := json.Unmarshal(embeddedPresetsJSON, &presets); err != nil {
		t.Fatalf("failed to unmarshal embedded presets: %v", err)
	}
	if len(presets) == 0 {
		t.Fatal("expected at least one preset")
	}

	// Check all presets have valid IDs and names
	ids := make(map[string]bool)
	for _, p := range presets {
		if p.ID == "" {
			t.Errorf("preset has empty ID: %+v", p)
		}
		if p.Name == "" {
			t.Errorf("preset %q has empty name", p.ID)
		}
		if ids[p.ID] {
			t.Errorf("duplicate preset ID: %q", p.ID)
		}
		ids[p.ID] = true
	}
}

func TestPresetRankingCriteriaMatchKnownIDs(t *testing.T) {
	var presets []Preset
	if err := json.Unmarshal(embeddedPresetsJSON, &presets); err != nil {
		t.Fatalf("failed to unmarshal embedded presets: %v", err)
	}

	// Build set of known criterion IDs from defaults
	knownIDs := make(map[RankingCriterionID]bool)
	for _, c := range DefaultRankingCriteria() {
		knownIDs[c.ID] = true
	}

	for _, p := range presets {
		for _, c := range p.Ranking.Criteria {
			if !knownIDs[c.ID] {
				t.Errorf("preset %q has unknown ranking criterion ID %q", p.ID, c.ID)
			}
		}
		// Ensure all known IDs are present in each preset
		presetIDs := make(map[RankingCriterionID]bool)
		for _, c := range p.Ranking.Criteria {
			presetIDs[c.ID] = true
		}
		for id := range knownIDs {
			if !presetIDs[id] {
				t.Errorf("preset %q is missing ranking criterion %q", p.ID, id)
			}
		}
	}
}

func TestLoadPresetsReturnsResults(t *testing.T) {
	presets := LoadPresets()
	if len(presets) == 0 {
		t.Fatal("LoadPresets() returned no presets")
	}
}
