package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/internal/datastore"
	"novastream/models"
)

// SportsLinksHandler serves the "Manage Team Channels" feature: CRUD over which Live TV
// channels are permanently linked (primary + backups) to which team, plus the batched
// lookup GameCard uses to show a quick-play button for teams that already have a link.
// Global/shared state, not per-user - see the sports feature plan for why.
type SportsLinksHandler struct {
	links       datastore.SportsLinksRepository
	liveHandler *LiveHandler
	config      *config.Manager
}

func (h *SportsLinksHandler) SetConfigManager(manager *config.Manager) { h.config = manager }

// NewSportsLinksHandler creates a new sports links handler.
func NewSportsLinksHandler(links datastore.SportsLinksRepository, liveHandler *LiveHandler) *SportsLinksHandler {
	return &SportsLinksHandler{links: links, liveHandler: liveHandler}
}

// channelLinkRequest is the request body for setting a primary or adding a backup link.
type channelLinkRequest struct {
	ChannelTvgID string `json:"channelTvgId"`
	ChannelName  string `json:"channelName"`
	ChannelURL   string `json:"channelUrl"`
	ChannelLogo  string `json:"channelLogo"`
	SourceID     string `json:"sourceId"`
	SourceName   string `json:"sourceName"`
}

func (req channelLinkRequest) valid() bool {
	return strings.TrimSpace(req.ChannelName) != "" && strings.TrimSpace(req.ChannelURL) != ""
}

// resolveChannelSet builds lookup sets (by normalized tvg-id and by normalized channel
// name) from the caller's current live channel list, so links can be marked
// Resolved/unresolved against what's actually in the playlist right now - see migration
// 056's comment on why links are a denormalized snapshot rather than a bare channel ID.
func resolveChannelSet(channels []LiveChannel) (byTvgID map[string]struct{}, byName map[string]struct{}) {
	byTvgID = make(map[string]struct{}, len(channels))
	byName = make(map[string]struct{}, len(channels))
	for _, ch := range channels {
		if tvgID := normalizeForMatch(ch.TvgID); tvgID != "" {
			byTvgID[tvgID] = struct{}{}
		}
		if name := normalizeForMatch(ch.Name); name != "" {
			byName[name] = struct{}{}
		}
	}
	return byTvgID, byName
}

func markResolved(link *models.SportsTeamChannelLink, byTvgID, byName map[string]struct{}) {
	if tvgID := normalizeForMatch(link.ChannelTvgID); tvgID != "" {
		if _, ok := byTvgID[tvgID]; ok {
			link.Resolved = true
			return
		}
	}
	if name := normalizeForMatch(link.ChannelName); name != "" {
		if _, ok := byName[name]; ok {
			link.Resolved = true
			return
		}
	}
	link.Resolved = false
}

// GetTeams lists all teams tracked for a league (?league=mlb, required) with their current
// links, resolved against the caller's live channel list.
func (h *SportsLinksHandler) GetTeams(w http.ResponseWriter, r *http.Request) {
	league := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("league")))
	if league == "" {
		http.Error(w, `{"error":"missing league"}`, http.StatusBadRequest)
		return
	}

	teams, err := h.links.ListTeamsByLeague(r.Context(), league)
	if err != nil {
		log.Printf("[sports-links] GetTeams: list teams failed: %v", err)
		http.Error(w, `{"error":"failed to list teams"}`, http.StatusInternalServerError)
		return
	}

	teamIDs := make([]string, len(teams))
	for i, t := range teams {
		teamIDs[i] = t.ID
	}
	linksByTeam, err := h.links.GetLinksForTeams(r.Context(), teamIDs)
	if err != nil {
		log.Printf("[sports-links] GetTeams: get links failed: %v", err)
		http.Error(w, `{"error":"failed to load channel links"}`, http.StatusInternalServerError)
		return
	}

	byTvgID, byName := h.currentChannelSet(r)

	result := make([]models.SportsTeamWithLinks, 0, len(teams))
	for _, t := range teams {
		links := linksByTeam[t.ID]
		for i := range links {
			markResolved(&links[i], byTvgID, byName)
		}
		result = append(result, models.SportsTeamWithLinks{SportsTeamRecord: t, Links: nonNilLinks(links)})
	}
	writeSportsJSON(w, map[string]any{"teams": result})
}

// currentChannelSet fetches the caller's live channel list once, tolerating failure (an
// unreachable playlist source shouldn't block viewing/managing links - it just means
// nothing can be marked Resolved this request).
func (h *SportsLinksHandler) currentChannelSet(r *http.Request) (byTvgID, byName map[string]struct{}) {
	if h.liveHandler == nil {
		return map[string]struct{}{}, map[string]struct{}{}
	}
	channels, err := h.liveHandler.FetchFilteredChannelsForRequest(r)
	if err != nil {
		log.Printf("[sports-links] failed to fetch live channels for resolution: %v", err)
		return map[string]struct{}{}, map[string]struct{}{}
	}
	return resolveChannelSet(channels)
}

// GetTeamLinks returns one team's current channel links.
func (h *SportsLinksHandler) GetTeamLinks(w http.ResponseWriter, r *http.Request) {
	teamID := strings.TrimSpace(mux.Vars(r)["teamId"])
	if teamID == "" {
		http.Error(w, `{"error":"missing team id"}`, http.StatusBadRequest)
		return
	}
	links, err := h.links.GetLinksForTeam(r.Context(), teamID)
	if err != nil {
		log.Printf("[sports-links] GetTeamLinks failed: %v", err)
		http.Error(w, `{"error":"failed to load channel links"}`, http.StatusInternalServerError)
		return
	}
	byTvgID, byName := h.currentChannelSet(r)
	for i := range links {
		markResolved(&links[i], byTvgID, byName)
	}
	writeSportsJSON(w, map[string]any{"links": nonNilLinks(links)})
}

// GetChannelSuggestions returns confidence-ranked Live TV channels for one team. An
// optional query keeps manual selection available while still scoring each text match.
func (h *SportsLinksHandler) GetChannelSuggestions(w http.ResponseWriter, r *http.Request) {
	teamID := strings.TrimSpace(mux.Vars(r)["teamId"])
	if teamID == "" {
		http.Error(w, `{"error":"missing team id"}`, http.StatusBadRequest)
		return
	}
	team, err := h.links.GetTeam(r.Context(), teamID)
	if err != nil {
		log.Printf("[sports-links] GetChannelSuggestions: get team failed: %v", err)
		http.Error(w, `{"error":"failed to look up team"}`, http.StatusInternalServerError)
		return
	}
	if team == nil {
		http.Error(w, `{"error":"team not found"}`, http.StatusNotFound)
		return
	}
	if h.liveHandler == nil {
		writeSportsJSON(w, map[string]any{"candidates": []models.SportsChannelCandidate{}})
		return
	}
	channels, err := h.liveHandler.FetchFilteredChannelsForRequest(r)
	if err != nil {
		log.Printf("[sports-links] GetChannelSuggestions: fetch channels failed: %v", err)
		http.Error(w, `{"error":"failed to fetch live channels"}`, http.StatusBadGateway)
		return
	}
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 500 {
			http.Error(w, `{"error":"limit must be between 1 and 500"}`, http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	candidates := rankTeamChannelCandidates(*team, channels, query, limit)
	writeSportsJSON(w, map[string]any{"candidates": candidates})
}

type sportsAutoLinkEvaluation struct {
	preview models.SportsAutoLinkPreview
	links   []models.SportsTeamChannelLink
}

func evaluateSportsAutoLinks(league string, teams []models.SportsTeamRecord, linksByTeam map[string][]models.SportsTeamChannelLink, channels []LiveChannel) sportsAutoLinkEvaluation {
	evaluation := sportsAutoLinkEvaluation{
		preview: models.SportsAutoLinkPreview{League: league, Results: []models.SportsAutoLinkTeamResult{}},
		links:   []models.SportsTeamChannelLink{},
	}
	channelByKey := buildChannelKeyIndex(channels)
	for _, team := range teams {
		result := models.SportsAutoLinkTeamResult{Team: team}
		for _, link := range linksByTeam[team.ID] {
			if link.Slot == models.SportsLinkSlotPrimary {
				result.SkipReason = "Primary channel already linked"
				break
			}
		}
		if result.SkipReason == "" {
			candidates := autoLinkCandidatesForTeam(team, channels, channelByKey)
			if len(candidates) == 0 {
				result.SkipReason = "No permanent team channel candidate"
			} else {
				best := candidates[0]
				result.Candidate = &best
				if len(candidates) > 1 {
					result.RunnerUpConfidence = candidates[1].Confidence
				}
				result.ConfidenceGap = roundSportsConfidence(best.Confidence - result.RunnerUpConfidence)
				result.Eligible = best.Confidence >= 0.95 ||
					(best.Confidence >= 0.85 && result.ConfidenceGap >= 0.20)
				if !result.Eligible {
					result.SkipReason = "Confidence or lead over runner-up is too low"
				} else {
					evaluation.links = append(evaluation.links, models.SportsTeamChannelLink{
						TeamID: team.ID, ChannelTvgID: best.ChannelTvgID, ChannelName: best.ChannelName,
						ChannelURL: best.ChannelURL, ChannelLogo: best.ChannelLogo, SourceID: best.SourceID,
						SourceName: best.SourceName, AutoLinked: true, LinkConfidence: best.Confidence,
						MatchReason: best.MatchReason,
					})
				}
			}
		}
		if result.Eligible {
			evaluation.preview.EligibleCount++
		} else {
			evaluation.preview.SkippedCount++
		}
		evaluation.preview.Results = append(evaluation.preview.Results, result)
	}
	return evaluation
}

func (h *SportsLinksHandler) evaluateLeagueAutoLinks(r *http.Request) (sportsAutoLinkEvaluation, error) {
	league := strings.ToLower(strings.TrimSpace(mux.Vars(r)["leagueId"]))
	if league == "" {
		return sportsAutoLinkEvaluation{}, fmt.Errorf("missing league id")
	}
	teams, err := h.links.ListTeamsByLeague(r.Context(), league)
	if err != nil {
		return sportsAutoLinkEvaluation{}, fmt.Errorf("load %s team catalog: %w", league, err)
	}
	if len(teams) == 0 {
		return sportsAutoLinkEvaluation{preview: models.SportsAutoLinkPreview{League: league, Message: "No teams are available for this league yet. Refresh sports data and try again.", Results: []models.SportsAutoLinkTeamResult{}}, links: []models.SportsTeamChannelLink{}}, nil
	}
	teamIDs := make([]string, len(teams))
	for i := range teams {
		teamIDs[i] = teams[i].ID
	}
	linksByTeam, err := h.links.GetLinksForTeams(r.Context(), teamIDs)
	if err != nil {
		return sportsAutoLinkEvaluation{}, fmt.Errorf("load existing team links: %w", err)
	}
	var channels []LiveChannel
	if h.liveHandler != nil {
		channels, err = h.liveHandler.FetchFilteredChannelsForRequest(r)
		if err != nil {
			return sportsAutoLinkEvaluation{}, fmt.Errorf("load Live TV channels: %w", err)
		}
	}
	if h.config != nil {
		if settings, loadErr := h.config.Load(); loadErr == nil {
			sourceIDs := settings.Sports.DefaultSourceIDs
			categoryIDs := settings.Sports.DefaultCategoryIDs
			if override, ok := settings.Sports.LeagueSearchOverrides[league]; ok {
				if len(override.SourceIDs) > 0 {
					sourceIDs = override.SourceIDs
				}
				if len(override.CategoryIDs) > 0 {
					categoryIDs = override.CategoryIDs
				}
			}
			channels = filterSportsChannelsByScope(channels, sourceIDs, categoryIDs)
		}
	}
	return evaluateSportsAutoLinks(league, teams, linksByTeam, channels), nil
}

// PreviewAutoLinks reports exactly which currently-unlinked teams satisfy the conservative
// permanent-channel confidence rules. It never writes links.
func (h *SportsLinksHandler) PreviewAutoLinks(w http.ResponseWriter, r *http.Request) {
	evaluation, err := h.evaluateLeagueAutoLinks(r)
	if err != nil {
		log.Printf("[sports-links] PreviewAutoLinks failed: %v", err)
		writeSportsJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": "Automatic link preview failed: " + err.Error(), "stage": "preview"})
		return
	}
	writeSportsJSON(w, evaluation.preview)
}

// ApplyAutoLinks re-evaluates immediately before inserting. The repository uses one
// conflict-safe INSERT, so a primary added after preview is preserved rather than replaced.
func (h *SportsLinksHandler) ApplyAutoLinks(w http.ResponseWriter, r *http.Request) {
	evaluation, err := h.evaluateLeagueAutoLinks(r)
	if err != nil {
		log.Printf("[sports-links] ApplyAutoLinks evaluation failed: %v", err)
		writeSportsJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": "Automatic link evaluation failed: " + err.Error(), "stage": "evaluate"})
		return
	}
	saved, err := h.links.InsertAutoPrimaryLinks(r.Context(), evaluation.links)
	if err != nil {
		log.Printf("[sports-links] ApplyAutoLinks insert failed: %v", err)
		writeSportsJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": "Automatic link save failed: " + err.Error(), "stage": "save"})
		return
	}
	for i := range saved {
		saved[i].Resolved = true
	}
	writeSportsJSON(w, map[string]any{"linkedCount": len(saved), "links": saved, "preview": evaluation.preview})
}

func writeSportsJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// nonNilLinks ensures a links slice serializes as JSON [] rather than null when empty -
// repository methods return nil for "no rows", but Go's nil-slice-marshals-to-null default
// forces every frontend consumer to null-check before .filter()/.find()/.map() rather than
// treating "no links yet" the same as "links: []".
func nonNilLinks(links []models.SportsTeamChannelLink) []models.SportsTeamChannelLink {
	if links == nil {
		return []models.SportsTeamChannelLink{}
	}
	return links
}

// SetPrimaryLink sets (or replaces) a team's primary channel.
func (h *SportsLinksHandler) SetPrimaryLink(w http.ResponseWriter, r *http.Request) {
	teamID := strings.TrimSpace(mux.Vars(r)["teamId"])
	if teamID == "" {
		http.Error(w, `{"error":"missing team id"}`, http.StatusBadRequest)
		return
	}
	var req channelLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.valid() {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	team, err := h.links.GetTeam(r.Context(), teamID)
	if err != nil {
		log.Printf("[sports-links] SetPrimaryLink: get team failed: %v", err)
		http.Error(w, `{"error":"failed to look up team"}`, http.StatusInternalServerError)
		return
	}
	if team == nil {
		http.Error(w, `{"error":"team not found"}`, http.StatusNotFound)
		return
	}
	saved, err := h.links.SetPrimaryLink(r.Context(), teamID, models.SportsTeamChannelLink{
		ChannelTvgID: req.ChannelTvgID,
		ChannelName:  req.ChannelName,
		ChannelURL:   req.ChannelURL,
		ChannelLogo:  req.ChannelLogo,
		SourceID:     req.SourceID,
		SourceName:   req.SourceName,
	})
	if err != nil {
		log.Printf("[sports-links] SetPrimaryLink failed: %v", err)
		http.Error(w, `{"error":"failed to save primary channel"}`, http.StatusInternalServerError)
		return
	}
	saved.Resolved = true // just set by the caller from their own live channel list
	writeSportsJSON(w, saved)
}

// AddBackupLink appends a backup channel to a team.
func (h *SportsLinksHandler) AddBackupLink(w http.ResponseWriter, r *http.Request) {
	teamID := strings.TrimSpace(mux.Vars(r)["teamId"])
	if teamID == "" {
		http.Error(w, `{"error":"missing team id"}`, http.StatusBadRequest)
		return
	}
	var req channelLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.valid() {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	team, err := h.links.GetTeam(r.Context(), teamID)
	if err != nil {
		log.Printf("[sports-links] AddBackupLink: get team failed: %v", err)
		http.Error(w, `{"error":"failed to look up team"}`, http.StatusInternalServerError)
		return
	}
	if team == nil {
		http.Error(w, `{"error":"team not found"}`, http.StatusNotFound)
		return
	}
	saved, err := h.links.AddBackupLink(r.Context(), teamID, models.SportsTeamChannelLink{
		ChannelTvgID: req.ChannelTvgID,
		ChannelName:  req.ChannelName,
		ChannelURL:   req.ChannelURL,
		ChannelLogo:  req.ChannelLogo,
		SourceID:     req.SourceID,
		SourceName:   req.SourceName,
	})
	if err != nil {
		log.Printf("[sports-links] AddBackupLink failed: %v", err)
		http.Error(w, `{"error":"failed to add backup channel"}`, http.StatusInternalServerError)
		return
	}
	saved.Resolved = true
	writeSportsJSON(w, saved)
}

// DeleteLink removes a channel link (primary or backup) from a team.
func (h *SportsLinksHandler) DeleteLink(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	teamID := strings.TrimSpace(vars["teamId"])
	linkID := strings.TrimSpace(vars["linkId"])
	if teamID == "" || linkID == "" {
		http.Error(w, `{"error":"missing team or link id"}`, http.StatusBadRequest)
		return
	}
	if err := h.links.DeleteLink(r.Context(), teamID, linkID); err != nil {
		log.Printf("[sports-links] DeleteLink failed: %v", err)
		http.Error(w, `{"error":"failed to delete channel link"}`, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type reorderBackupsRequest struct {
	LinkIDs []string `json:"linkIds"`
}

// ReorderBackups rewrites a team's backup channel order.
func (h *SportsLinksHandler) ReorderBackups(w http.ResponseWriter, r *http.Request) {
	teamID := strings.TrimSpace(mux.Vars(r)["teamId"])
	if teamID == "" {
		http.Error(w, `{"error":"missing team id"}`, http.StatusBadRequest)
		return
	}
	var req reorderBackupsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.LinkIDs) == 0 {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if err := h.links.ReorderBackups(r.Context(), teamID, req.LinkIDs); err != nil {
		log.Printf("[sports-links] ReorderBackups failed: %v", err)
		http.Error(w, `{"error":"failed to reorder backup channels"}`, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetLinkedChannels is the batched lookup GameCard uses: given several team IDs (a
// scoreboard page's worth of games), return each team's current links in one round trip
// instead of one request per team.
func (h *SportsLinksHandler) GetLinkedChannels(w http.ResponseWriter, r *http.Request) {
	idsParam := strings.TrimSpace(r.URL.Query().Get("ids"))
	if idsParam == "" {
		writeSportsJSON(w, map[string]any{"linksByTeam": map[string][]models.SportsTeamChannelLink{}})
		return
	}
	teamIDs := strings.Split(idsParam, ",")
	for i := range teamIDs {
		teamIDs[i] = strings.TrimSpace(teamIDs[i])
	}
	linksByTeam, err := h.links.GetLinksForTeams(r.Context(), teamIDs)
	if err != nil {
		log.Printf("[sports-links] GetLinkedChannels failed: %v", err)
		http.Error(w, `{"error":"failed to load channel links"}`, http.StatusInternalServerError)
		return
	}
	byTvgID, byName := h.currentChannelSet(r)
	for teamID, links := range linksByTeam {
		for i := range links {
			markResolved(&links[i], byTvgID, byName)
		}
		linksByTeam[teamID] = links
	}
	writeSportsJSON(w, map[string]any{"linksByTeam": linksByTeam})
}

// Options handles CORS preflight requests.
func (h *SportsLinksHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// SyncTeamsFromGames upserts every team appearing in the given games into sports_teams,
// keyed on league+ESPN team ID. This remains a fallback alongside the full team catalog,
// and also keeps identities current from the frequently refreshed scoreboard payload.
func (h *SportsLinksHandler) SyncTeamsFromGames(ctx context.Context, games []models.SportsGame) {
	teams := make([]models.SportsTeamRecord, 0, len(games)*2)
	for _, game := range games {
		for _, team := range []models.SportsTeam{game.HomeTeam, game.AwayTeam} {
			if team.ID == "" {
				continue
			}
			teams = append(teams, models.SportsTeamRecord{
				ID:           fmt.Sprintf("%s:%s", game.League, team.ID),
				League:       game.League,
				EspnTeamID:   team.ID,
				Name:         team.Name,
				Location:     team.Location,
				Nickname:     team.Nickname,
				Abbreviation: team.Abbreviation,
				LogoURL:      team.LogoURL,
			})
		}
	}
	h.SyncTeams(ctx, teams)
}

// SyncTeams upserts a complete league catalog (or any other batch of team identities),
// de-duplicating it before touching the datastore.
func (h *SportsLinksHandler) SyncTeams(ctx context.Context, teams []models.SportsTeamRecord) {
	seen := make(map[string]struct{}, len(teams))
	for _, team := range teams {
		if ctx.Err() != nil {
			return
		}
		if team.ID == "" {
			continue
		}
		if _, ok := seen[team.ID]; ok {
			continue
		}
		seen[team.ID] = struct{}{}
		if err := h.links.UpsertTeam(ctx, team); err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				log.Printf("[sports-links] SyncTeams: batch interrupted: %v", err)
				return
			}
			log.Printf("[sports-links] SyncTeams: upsert team %s failed: %v", team.ID, err)
		}
	}
}
