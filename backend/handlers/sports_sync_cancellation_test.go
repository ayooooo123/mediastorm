package handlers

import (
	"context"
	"fmt"
	"novastream/internal/datastore"
	"novastream/models"
	"testing"
)

type cancelTeamRepository struct {
	datastore.SportsLinksRepository
	calls  int
	cancel context.CancelFunc
	err    error
}

func (r *cancelTeamRepository) UpsertTeam(ctx context.Context, team models.SportsTeamRecord) error {
	r.calls++
	if r.cancel != nil {
		r.cancel()
	}
	if r.err != nil {
		return r.err
	}
	return ctx.Err()
}
func TestSyncTeamsStopsOnCancellation(t *testing.T) {
	for _, alreadyCanceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		repo := &cancelTeamRepository{cancel: cancel}
		if alreadyCanceled {
			cancel()
		}
		h := &SportsLinksHandler{links: repo}
		h.SyncTeams(ctx, []models.SportsTeamRecord{{ID: "one"}, {ID: "two"}, {ID: "three"}})
		want := 1
		if alreadyCanceled {
			want = 0
		}
		if repo.calls != want {
			t.Fatalf("canceled=%v: got %d writes, want %d", alreadyCanceled, repo.calls, want)
		}
		cancel()
	}
}

func TestSyncTeamsStopsOnRepositoryDeadline(t *testing.T) {
	repo := &cancelTeamRepository{err: fmt.Errorf("upsert sports team: %w", context.DeadlineExceeded)}
	h := &SportsLinksHandler{links: repo}
	h.SyncTeams(context.Background(), []models.SportsTeamRecord{{ID: "one"}, {ID: "two"}, {ID: "three"}})
	if repo.calls != 1 {
		t.Fatalf("got %d writes after deadline, want 1", repo.calls)
	}
}

func TestSyncTeamsContinuesUnrelatedErrorsAndDeduplicates(t *testing.T) {
	repo := &cancelTeamRepository{err: fmt.Errorf("invalid team record")}
	h := &SportsLinksHandler{links: repo}
	h.SyncTeams(context.Background(), []models.SportsTeamRecord{{ID: "one"}, {ID: "one"}, {ID: ""}, {ID: "two"}})
	if repo.calls != 2 {
		t.Fatalf("got %d writes, want 2 unique teams", repo.calls)
	}
}

// The background refresh hands the whole catalog to SyncTeams every 90 s;
// only new or changed teams may reach the database.
func TestSyncTeamsSkipsUnchangedTeams(t *testing.T) {
	repo := &cancelTeamRepository{}
	h := &SportsLinksHandler{links: repo}
	catalog := []models.SportsTeamRecord{{ID: "nba:1", Name: "Lakers"}, {ID: "nba:2", Name: "Celtics"}}
	h.SyncTeams(context.Background(), catalog)
	h.SyncTeams(context.Background(), catalog)
	if repo.calls != 2 {
		t.Fatalf("got %d writes for an unchanged catalog, want 2 (first sync only)", repo.calls)
	}
	catalog[1].LogoURL = "https://example.test/celtics.png"
	h.SyncTeams(context.Background(), catalog)
	if repo.calls != 3 {
		t.Fatalf("got %d writes, want 3 (one changed team)", repo.calls)
	}

	// A failed write is tried again on the next sync.
	failing := &cancelTeamRepository{err: fmt.Errorf("invalid team record")}
	h = &SportsLinksHandler{links: failing}
	h.SyncTeams(context.Background(), catalog[:1])
	h.SyncTeams(context.Background(), catalog[:1])
	if failing.calls != 2 {
		t.Fatalf("got %d attempts, want a retry after a failed write", failing.calls)
	}
}

// Scoreboard and catalog describe some teams slightly differently; the
// scoreboard must not overwrite a team the catalog already wrote.
func TestSyncTeamsFromGamesOnlyAddsNewTeams(t *testing.T) {
	repo := &cancelTeamRepository{}
	h := &SportsLinksHandler{links: repo}
	h.SyncTeams(context.Background(), []models.SportsTeamRecord{{ID: "mlb:30", League: "mlb", EspnTeamID: "30", Name: "Tampa Bay Rays", LogoURL: "https://a.espncdn.com/i/teamlogos/mlb/500/tb.png"}})
	games := []models.SportsGame{{
		League:   "mlb",
		HomeTeam: models.SportsTeam{ID: "30", Name: "Tampa Bay Rays", LogoURL: "https://a.espncdn.com/i/teamlogos/mlb/500/scoreboard/tb.png"},
		AwayTeam: models.SportsTeam{ID: "10", Name: "New York Yankees"},
	}}
	for i := 0; i < 3; i++ {
		h.SyncTeamsFromGames(context.Background(), games)
		h.SyncTeams(context.Background(), []models.SportsTeamRecord{{ID: "mlb:30", League: "mlb", EspnTeamID: "30", Name: "Tampa Bay Rays", LogoURL: "https://a.espncdn.com/i/teamlogos/mlb/500/tb.png"}})
	}
	if repo.calls != 2 {
		t.Fatalf("got %d writes, want 2 (the catalog team once, the team only the scoreboard knows once)", repo.calls)
	}
}
