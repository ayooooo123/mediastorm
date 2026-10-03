package handlers

import (
	"context"
	"log"
	"time"
)

// RefreshBackground performs one scheduled score and team refresh, honoring the
// current server-wide settings before each stage.
func (h *SportsHandler) RefreshBackground(ctx context.Context, links *SportsLinksHandler) {
	if h.config == nil || ctx.Err() != nil {
		return
	}
	// Re-check between stages as well as each tick: a disabled server must not
	// keep synchronizing cached teams after its current network request finishes.
	enabled := func() bool {
		if ctx.Err() != nil {
			return false
		}
		liveSettings, err := h.config.Load()
		if err != nil {
			log.Printf("[sports] skipping background updates: settings unavailable: %v", err)
			return false
		}
		ids := liveSettings.Sports.PollingLeagueIDs()
		h.service.SetEnabledLeagueIDs(ids)
		return h.service.GetStatus().Enabled
	}

	if !enabled() {
		return
	}
	refreshCtx, cancelRefresh := context.WithTimeout(ctx, 15*time.Second)
	if err := h.service.Refresh(refreshCtx); err != nil {
		log.Printf("[sports] refresh error: %v", err)
	}
	cancelRefresh()

	if !enabled() {
		return
	}
	syncGamesCtx, cancelSyncGames := context.WithTimeout(ctx, 10*time.Second)
	links.SyncTeamsFromGames(syncGamesCtx, h.service.GetScoreboard(""))
	cancelSyncGames()

	if !enabled() {
		return
	}
	catalogCtx, cancelCatalog := context.WithTimeout(ctx, 30*time.Second)
	catalog, err := h.service.EnsureTeamCatalog(catalogCtx)
	cancelCatalog()
	if err != nil {
		log.Printf("[sports] team catalog refresh error: %v", err)
	}
	if !enabled() {
		return
	}
	syncCatalogCtx, cancelSyncCatalog := context.WithTimeout(ctx, 10*time.Second)
	links.SyncTeams(syncCatalogCtx, catalog)
	cancelSyncCatalog()
}
