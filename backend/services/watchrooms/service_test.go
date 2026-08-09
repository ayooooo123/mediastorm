package watchrooms

import (
	"context"
	"testing"
	"time"

	"novastream/models"
)

type fakeProfiles map[string]models.User

func (p fakeProfiles) Get(id string) (models.User, bool) { user, ok := p[id]; return user, ok }

type fakeRoomRepo struct {
	room     *models.WatchRoom
	invites  map[string]bool
	members  map[string]models.WatchRoomMember
	stateSet bool
}

func (r *fakeRoomRepo) Create(_ context.Context, room *models.WatchRoom, invitees []string, clientID string) error {
	copy := *room
	r.room = &copy
	r.invites = map[string]bool{room.CreatorProfileID: true}
	for _, id := range invitees {
		r.invites[id] = true
	}
	r.members = map[string]models.WatchRoomMember{
		room.CreatorProfileID: {ProfileID: room.CreatorProfileID, IsCreator: true, Ready: true, Joined: true, ClientID: clientID, JoinedAt: room.CreatedAt, LastSeenAt: room.CreatedAt},
	}
	return nil
}
func (r *fakeRoomRepo) Get(_ context.Context, roomID string) (*models.WatchRoom, error) {
	if r.room == nil || r.room.ID != roomID {
		return nil, nil
	}
	copy := *r.room
	copy.Members = make([]models.WatchRoomMember, 0, len(r.invites))
	for _, member := range r.members {
		copy.Members = append(copy.Members, member)
	}
	for profileID := range r.invites {
		if _, joined := r.members[profileID]; !joined {
			copy.Members = append(copy.Members, models.WatchRoomMember{ProfileID: profileID})
		}
	}
	return &copy, nil
}
func (r *fakeRoomRepo) ListInvitations(_ context.Context, profileID string, _ time.Time) ([]models.WatchRoom, error) {
	if r.room == nil || !r.invites[profileID] {
		return []models.WatchRoom{}, nil
	}
	room, _ := r.Get(context.Background(), r.room.ID)
	return []models.WatchRoom{*room}, nil
}
func (r *fakeRoomRepo) IsInvited(_ context.Context, roomID, profileID string) (bool, error) {
	return r.room != nil && r.room.ID == roomID && r.invites[profileID], nil
}
func (r *fakeRoomRepo) Join(_ context.Context, _, profileID, clientID string, now time.Time) error {
	r.members[profileID] = models.WatchRoomMember{ProfileID: profileID, ClientID: clientID, Joined: true, JoinedAt: now, LastSeenAt: now}
	return nil
}
func (r *fakeRoomRepo) SetReady(_ context.Context, _, profileID string, ready bool, now time.Time) error {
	m := r.members[profileID]
	m.Ready = ready
	m.LastSeenAt = now
	r.members[profileID] = m
	return nil
}
func (r *fakeRoomRepo) UpdateState(_ context.Context, _, profileID, status string, position, duration float64, now time.Time) error {
	r.room.Status, r.room.Position, r.room.Duration = status, position, duration
	r.room.Revision++
	r.room.UpdatedBy = profileID
	r.room.AnchorUpdatedAt = now
	r.stateSet = true
	return nil
}
func (r *fakeRoomRepo) Heartbeat(_ context.Context, _, profileID, clientID string, buffering bool, now time.Time) error {
	m := r.members[profileID]
	m.ClientID = clientID
	m.Buffering = buffering
	m.LastSeenAt = now
	r.members[profileID] = m
	return nil
}
func (r *fakeRoomRepo) Leave(_ context.Context, _, profileID string) error {
	delete(r.members, profileID)
	return nil
}
func (r *fakeRoomRepo) End(_ context.Context, _, profileID string, now time.Time) (bool, error) {
	if r.room.CreatorProfileID != profileID {
		return false, nil
	}
	r.room.Status = models.WatchRoomStatusEnded
	r.room.AnchorUpdatedAt = now
	return true, nil
}

func TestCreateJoinAndUpdateWatchRoom(t *testing.T) {
	repo := &fakeRoomRepo{}
	svc := New(repo, fakeProfiles{
		"host":  {ID: "host", Name: "Host"},
		"guest": {ID: "guest", Name: "Guest"},
		"other": {ID: "other", Name: "Other"},
	})
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	room, err := svc.Create(context.Background(), "host", models.WatchRoomCreate{
		Title: "Movie", MediaType: "movie", ItemID: "tmdb:movie:1",
		InviteeProfileIDs: []string{"guest", "missing"}, ClientID: "host-tv",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if room.Status != models.WatchRoomStatusLobby || !repo.invites["guest"] || repo.invites["missing"] {
		t.Fatalf("unexpected created room: %#v invites=%v", room, repo.invites)
	}
	if len(room.Members) != 2 {
		t.Fatalf("created room members = %#v, want joined host and invited guest", room.Members)
	}
	foundJoinedHost := false
	foundInvitedGuest := false
	for _, member := range room.Members {
		if member.ProfileID == "host" && member.Joined {
			foundJoinedHost = true
		}
		if member.ProfileID == "guest" && !member.Joined {
			foundInvitedGuest = true
		}
	}
	if !foundJoinedHost || !foundInvitedGuest {
		t.Fatalf("created room did not distinguish joined and invited members: %#v", room.Members)
	}
	otherInvitations, err := svc.Invitations(context.Background(), "other")
	if err != nil {
		t.Fatalf("Invitations(other) error = %v", err)
	}
	if len(otherInvitations) != 0 {
		t.Fatalf("unselected profile received %d invitation(s), want 0", len(otherInvitations))
	}

	joined, err := svc.Join(context.Background(), room.ID, "guest", "guest-tv")
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}
	if len(joined.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(joined.Members))
	}
	for _, member := range joined.Members {
		if member.ProfileID == "guest" && !member.Joined {
			t.Fatal("guest remained unjoined after Join")
		}
	}
	if _, err := svc.SetReady(context.Background(), room.ID, "guest", true); err != nil {
		t.Fatalf("SetReady() error = %v", err)
	}
	if _, err := svc.UpdateState(context.Background(), room.ID, "guest", models.WatchRoomStateUpdate{Status: "playing", Position: 10, Duration: 100}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}
	if !repo.stateSet {
		t.Fatal("state was not persisted")
	}

	now = now.Add(5 * time.Second)
	updated, err := svc.Get(context.Background(), room.ID, "guest")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if updated.Position != 15 {
		t.Fatalf("effective position = %v, want 15", updated.Position)
	}
}

func TestUpdateStateRejectsNonMemberAndInvalidValues(t *testing.T) {
	repo := &fakeRoomRepo{}
	svc := New(repo, fakeProfiles{"host": {ID: "host"}, "guest": {ID: "guest"}})
	svc.now = func() time.Time { return time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC) }
	room, err := svc.Create(context.Background(), "host", models.WatchRoomCreate{Title: "Movie", MediaType: "movie", ItemID: "one", InviteeProfileIDs: []string{"guest"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateState(context.Background(), room.ID, "guest", models.WatchRoomStateUpdate{Status: "playing"}); err != ErrNotMember {
		t.Fatalf("non-member error = %v, want %v", err, ErrNotMember)
	}
	if _, err := svc.UpdateState(context.Background(), room.ID, "host", models.WatchRoomStateUpdate{Status: "lobby"}); err != ErrInvalidState {
		t.Fatalf("invalid state error = %v, want %v", err, ErrInvalidState)
	}
}
