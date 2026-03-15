package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed presets.json
var embeddedPresetsJSON []byte

// Preset defines a pre-selectable filtering & ranking configuration.
type Preset struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Filtering   FilterSettings  `json:"filtering"`
	Ranking     RankingSettings `json:"ranking"`
}

// LoadPresets loads presets from a runtime file (config/presets.json) if available,
// falling back to the embedded defaults.
func LoadPresets() []Preset {
	// Try runtime file first (for Docker volume overrides)
	runtimePath := filepath.Join("config", "presets.json")
	if data, err := os.ReadFile(runtimePath); err == nil {
		var presets []Preset
		if err := json.Unmarshal(data, &presets); err == nil && len(presets) > 0 {
			return presets
		}
		fmt.Printf("Warning: failed to parse runtime presets file %s, using defaults: %v\n", runtimePath, err)
	}

	// Fall back to embedded defaults
	var presets []Preset
	if err := json.Unmarshal(embeddedPresetsJSON, &presets); err != nil {
		fmt.Printf("Warning: failed to parse embedded presets: %v\n", err)
		return nil
	}
	return presets
}
