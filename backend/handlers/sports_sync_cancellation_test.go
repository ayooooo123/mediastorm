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
