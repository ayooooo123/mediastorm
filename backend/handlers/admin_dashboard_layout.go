package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const (
	adminDashboardLayoutVersion = 1
	adminDashboardColumns       = 12
	adminDashboardMaxRows       = 512
	adminDashboardMaxBodyBytes  = 32 << 10
	adminDashboardLayoutFile    = "admin-dashboard-layout.json"
)

type adminDashboardLayoutModule struct {
	ID string `json:"id"`
	X  int    `json:"x"`
	Y  int    `json:"y"`
	W  int    `json:"w"`
	H  int    `json:"h"`
}

type adminDashboardLayout struct {
	Version int                          `json:"version"`
	Modules []adminDashboardLayoutModule `json:"modules"`
}

type adminDashboardModuleDefinition struct {
	ID       string `json:"id"`
	MinW     int    `json:"minW"`
	MinH     int    `json:"minH"`
	MaxW     int    `json:"maxW"`
	MaxH     int    `json:"maxH"`
	Advanced bool   `json:"advanced,omitempty"`
	DefaultX int    `json:"defaultX"`
	DefaultY int    `json:"defaultY"`
	DefaultW int    `json:"defaultW"`
	DefaultH int    `json:"defaultH"`
}

type adminDashboardLayoutResponse struct {
	Version     int                              `json:"version"`
	Columns     int                              `json:"columns"`
	Modules     []adminDashboardLayoutModule     `json:"modules"`
	Definitions []adminDashboardModuleDefinition `json:"definitions"`
}

var adminDashboardModules = []adminDashboardModuleDefinition{
	{ID: "system-status", MinW: 2, MinH: 2, MaxW: 6, MaxH: 3, DefaultX: 0, DefaultY: 0, DefaultW: 3, DefaultH: 2},
	{ID: "active-stream-count", MinW: 2, MinH: 2, MaxW: 6, MaxH: 3, DefaultX: 3, DefaultY: 0, DefaultW: 3, DefaultH: 2},
	{ID: "backend-uptime", MinW: 2, MinH: 2, MaxW: 6, MaxH: 3, DefaultX: 6, DefaultY: 0, DefaultW: 3, DefaultH: 2},
	{ID: "provider-readiness", MinW: 2, MinH: 2, MaxW: 6, MaxH: 3, DefaultX: 9, DefaultY: 0, DefaultW: 3, DefaultH: 2},
	{ID: "active-streams", MinW: 6, MinH: 5, MaxW: 12, MaxH: 12, DefaultX: 0, DefaultY: 2, DefaultW: 8, DefaultH: 7},
	{ID: "provider-health", MinW: 3, MinH: 5, MaxW: 8, MaxH: 10, DefaultX: 8, DefaultY: 2, DefaultW: 4, DefaultH: 7},
	{ID: "watch-time", MinW: 5, MinH: 5, MaxW: 12, MaxH: 10, DefaultX: 0, DefaultY: 9, DefaultW: 7, DefaultH: 6},
	{ID: "recently-watched", MinW: 4, MinH: 5, MaxW: 8, MaxH: 10, DefaultX: 7, DefaultY: 9, DefaultW: 5, DefaultH: 6},
	{ID: "usenet-connections", MinW: 8, MinH: 4, MaxW: 12, MaxH: 10, Advanced: true, DefaultX: 0, DefaultY: 15, DefaultW: 12, DefaultH: 6},
	{ID: "live-stream-limits", MinW: 8, MinH: 4, MaxW: 12, MaxH: 10, Advanced: true, DefaultX: 0, DefaultY: 21, DefaultW: 12, DefaultH: 6},
	{ID: "vod-stream-limits", MinW: 8, MinH: 4, MaxW: 12, MaxH: 10, Advanced: true, DefaultX: 0, DefaultY: 27, DefaultW: 12, DefaultH: 6},
	{ID: "usenet-providers", MinW: 4, MinH: 5, MaxW: 8, MaxH: 10, Advanced: true, DefaultX: 0, DefaultY: 33, DefaultW: 6, DefaultH: 7},
	{ID: "debrid-providers", MinW: 4, MinH: 5, MaxW: 8, MaxH: 10, Advanced: true, DefaultX: 6, DefaultY: 33, DefaultW: 6, DefaultH: 7},
	{ID: "endpoint-health", MinW: 8, MinH: 4, MaxW: 12, MaxH: 10, Advanced: true, DefaultX: 0, DefaultY: 40, DefaultW: 12, DefaultH: 6},
	{ID: "configuration-summary", MinW: 8, MinH: 6, MaxW: 12, MaxH: 12, Advanced: true, DefaultX: 0, DefaultY: 46, DefaultW: 12, DefaultH: 8},
	{ID: "indexers", MinW: 4, MinH: 5, MaxW: 8, MaxH: 10, Advanced: true, DefaultX: 0, DefaultY: 54, DefaultW: 6, DefaultH: 7},
	{ID: "torrent-scrapers", MinW: 4, MinH: 5, MaxW: 8, MaxH: 10, Advanced: true, DefaultX: 6, DefaultY: 54, DefaultW: 6, DefaultH: 7},
}

var adminDashboardLayoutMu sync.Mutex

func defaultAdminDashboardLayout() adminDashboardLayout {
	modules := make([]adminDashboardLayoutModule, 0, len(adminDashboardModules))
	for _, definition := range adminDashboardModules {
		modules = append(modules, adminDashboardLayoutModule{
			ID: definition.ID,
			X:  definition.DefaultX,
			Y:  definition.DefaultY,
			W:  definition.DefaultW,
			H:  definition.DefaultH,
		})
	}
	return adminDashboardLayout{Version: adminDashboardLayoutVersion, Modules: modules}
}

func adminDashboardDefinitionMap() map[string]adminDashboardModuleDefinition {
	definitions := make(map[string]adminDashboardModuleDefinition, len(adminDashboardModules))
	for _, definition := range adminDashboardModules {
		definitions[definition.ID] = definition
	}
	return definitions
}

func validateAdminDashboardLayout(layout adminDashboardLayout) (adminDashboardLayout, error) {
	if layout.Version != adminDashboardLayoutVersion {
		return adminDashboardLayout{}, fmt.Errorf("unsupported dashboard layout version %d", layout.Version)
	}
	if len(layout.Modules) != len(adminDashboardModules) {
		return adminDashboardLayout{}, fmt.Errorf("dashboard layout must contain exactly %d modules", len(adminDashboardModules))
	}

	definitions := adminDashboardDefinitionMap()
	seen := make(map[string]bool, len(layout.Modules))
	modules := append([]adminDashboardLayoutModule(nil), layout.Modules...)
	for _, module := range modules {
		definition, ok := definitions[module.ID]
		if !ok {
			return adminDashboardLayout{}, fmt.Errorf("unknown dashboard module %q", module.ID)
		}
		if seen[module.ID] {
			return adminDashboardLayout{}, fmt.Errorf("dashboard module %q appears more than once", module.ID)
		}
		seen[module.ID] = true
		if err := validateAdminDashboardModule(module, definition); err != nil {
			return adminDashboardLayout{}, err
		}
	}
	if dashboardModulesOverlap(modules) {
		return adminDashboardLayout{}, errors.New("dashboard modules must not overlap")
	}

	compactAdminDashboardModules(modules)
	return adminDashboardLayout{Version: adminDashboardLayoutVersion, Modules: modules}, nil
}

func validateAdminDashboardModule(module adminDashboardLayoutModule, definition adminDashboardModuleDefinition) error {
	if module.W < definition.MinW || module.W > definition.MaxW || module.H < definition.MinH || module.H > definition.MaxH {
		return fmt.Errorf("dashboard module %q has unsupported dimensions %dx%d", module.ID, module.W, module.H)
	}
	if module.X < 0 || module.Y < 0 || module.X > adminDashboardColumns-module.W || module.Y > adminDashboardMaxRows-module.H {
		return fmt.Errorf("dashboard module %q is outside the supported grid", module.ID)
	}
	return nil
}

func dashboardModulesOverlap(modules []adminDashboardLayoutModule) bool {
	for i := range modules {
		for j := i + 1; j < len(modules); j++ {
			if dashboardModulesIntersect(modules[i], modules[j]) {
				return true
			}
		}
	}
	return false
}

func dashboardModulesIntersect(a, b adminDashboardLayoutModule) bool {
	return a.X < b.X+b.W && a.X+a.W > b.X && a.Y < b.Y+b.H && a.Y+a.H > b.Y
}

func compactAdminDashboardModules(modules []adminDashboardLayoutModule) {
	order := make([]int, len(modules))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := modules[order[i]], modules[order[j]]
		if a.Y != b.Y {
			return a.Y < b.Y
		}
		return a.X < b.X
	})

	placed := make([]adminDashboardLayoutModule, 0, len(modules))
	for _, index := range order {
		module := modules[index]
		for module.Y > 0 {
			candidate := module
			candidate.Y--
			blocked := false
			for _, other := range placed {
				if dashboardModulesIntersect(candidate, other) {
					blocked = true
					break
				}
			}
			if blocked {
				break
			}
			module = candidate
		}
		modules[index] = module
		placed = append(placed, module)
	}
}

func normalizedStoredAdminDashboardLayout(stored *adminDashboardLayout) adminDashboardLayout {
	if stored == nil {
		return defaultAdminDashboardLayout()
	}
	if normalized, err := validateAdminDashboardLayout(*stored); err == nil {
		return normalized
	}

	definitions := adminDashboardDefinitionMap()
	seen := make(map[string]bool, len(stored.Modules))
	modules := make([]adminDashboardLayoutModule, 0, len(adminDashboardModules))
	maxBottom := 0
	for _, module := range stored.Modules {
		definition, ok := definitions[module.ID]
		if !ok || seen[module.ID] || validateAdminDashboardModule(module, definition) != nil {
			continue
		}
		seen[module.ID] = true
		modules = append(modules, module)
		if bottom := module.Y + module.H; bottom > maxBottom {
			maxBottom = bottom
		}
	}
	if dashboardModulesOverlap(modules) {
		return defaultAdminDashboardLayout()
	}

	firstMissingY := -1
	for _, definition := range adminDashboardModules {
		if !seen[definition.ID] && (firstMissingY < 0 || definition.DefaultY < firstMissingY) {
			firstMissingY = definition.DefaultY
		}
	}
	for _, definition := range adminDashboardModules {
		if seen[definition.ID] {
			continue
		}
		modules = append(modules, adminDashboardLayoutModule{
			ID: definition.ID,
			X:  definition.DefaultX,
			Y:  maxBottom + definition.DefaultY - firstMissingY,
			W:  definition.DefaultW,
			H:  definition.DefaultH,
		})
	}
	compactAdminDashboardModules(modules)
	return adminDashboardLayout{Version: adminDashboardLayoutVersion, Modules: modules}
}

func dashboardLayoutResponse(layout adminDashboardLayout) adminDashboardLayoutResponse {
	return adminDashboardLayoutResponse{
		Version:     layout.Version,
		Columns:     adminDashboardColumns,
		Modules:     layout.Modules,
		Definitions: append([]adminDashboardModuleDefinition(nil), adminDashboardModules...),
	}
}

func (h *AdminUIHandler) dashboardLayoutPath() (string, error) {
	if h.settingsPath == "" {
		return "", errors.New("settings path not available")
	}
	return filepath.Join(filepath.Dir(h.settingsPath), adminDashboardLayoutFile), nil
}

func loadAdminDashboardLayout(path string) (adminDashboardLayout, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultAdminDashboardLayout(), nil
	}
	if err != nil {
		return adminDashboardLayout{}, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, adminDashboardMaxBodyBytes+1))
	if err != nil {
		return adminDashboardLayout{}, err
	}
	if len(data) > adminDashboardMaxBodyBytes {
		return defaultAdminDashboardLayout(), nil
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var stored adminDashboardLayout
	if err := decoder.Decode(&stored); err != nil {
		return defaultAdminDashboardLayout(), nil
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return defaultAdminDashboardLayout(), nil
	}
	return normalizedStoredAdminDashboardLayout(&stored), nil
}

func saveAdminDashboardLayout(path string, layout adminDashboardLayout) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".admin-dashboard-layout-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(layout); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// GetDashboardLayout returns the shared, normalized administrator dashboard layout.
func (h *AdminUIHandler) GetDashboardLayout(w http.ResponseWriter, _ *http.Request) {
	path, err := h.dashboardLayoutPath()
	if err != nil {
		http.Error(w, "Dashboard layout storage not available", http.StatusInternalServerError)
		return
	}
	layout, err := loadAdminDashboardLayout(path)
	if err != nil {
		http.Error(w, "Failed to load dashboard layout", http.StatusInternalServerError)
		return
	}
	writeDashboardLayoutJSON(w, http.StatusOK, layout)
}

// SaveDashboardLayout validates and stores the shared administrator dashboard layout.
func (h *AdminUIHandler) SaveDashboardLayout(w http.ResponseWriter, r *http.Request) {
	path, err := h.dashboardLayoutPath()
	if err != nil {
		http.Error(w, "Dashboard layout storage not available", http.StatusInternalServerError)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, adminDashboardMaxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var submitted adminDashboardLayout
	if err := decoder.Decode(&submitted); err != nil {
		http.Error(w, "Invalid dashboard layout: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "Invalid dashboard layout: multiple JSON values", http.StatusBadRequest)
		return
	}

	normalized, err := validateAdminDashboardLayout(submitted)
	if err != nil {
		http.Error(w, "Invalid dashboard layout: "+err.Error(), http.StatusBadRequest)
		return
	}

	adminDashboardLayoutMu.Lock()
	defer adminDashboardLayoutMu.Unlock()
	if err := saveAdminDashboardLayout(path, normalized); err != nil {
		http.Error(w, "Failed to save dashboard layout", http.StatusInternalServerError)
		return
	}
	writeDashboardLayoutJSON(w, http.StatusOK, normalized)
}

// ResetDashboardLayout removes the saved arrangement and returns the built-in default.
func (h *AdminUIHandler) ResetDashboardLayout(w http.ResponseWriter, _ *http.Request) {
	path, err := h.dashboardLayoutPath()
	if err != nil {
		http.Error(w, "Dashboard layout storage not available", http.StatusInternalServerError)
		return
	}

	adminDashboardLayoutMu.Lock()
	defer adminDashboardLayoutMu.Unlock()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "Failed to reset dashboard layout", http.StatusInternalServerError)
		return
	}
	writeDashboardLayoutJSON(w, http.StatusOK, defaultAdminDashboardLayout())
}

func writeDashboardLayoutJSON(w http.ResponseWriter, status int, layout adminDashboardLayout) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(dashboardLayoutResponse(layout))
}
