package watchrooms

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"novastream/internal/datastore"
	"novastream/models"
)

var (
	ErrNotFound           = errors.New("watch room not found")
	ErrNotInvited         = errors.New("profile is not invited to this watch room")
	ErrNotMember          = errors.New("profile has not joined this watch room")
	ErrNotCreator         = errors.New("only the room creator can end this watch room")
	ErrInvalidMedia       = errors.New("title, mediaType, and itemId are required")
	ErrInvalidState       = errors.New("invalid watch room state")
	ErrRoomEnded          = errors.New("watch room has ended")
	ErrIncompatibleClient = errors.New("client does not support Watch Together native playback protocol")
)

const (
	roomLifetime     = 24 * time.Hour
	presenceTTL      = 15 * time.Second
	disconnectGrace  = 2 * time.Minute
	endedAuditWindow = 24 * time.Hour
	protocolVersion  = 1
)

type profileProvider interface {
	Get(id string) (models.User, bool)
}

type Service struct {
	repo     datastore.WatchRoomRepository
	profiles profileProvider
	now      func() time.Time
}

func New(repo datastore.WatchRoomRepository, profiles profileProvider) *Service {
	return &Service{repo: repo, profiles: profiles, now: time.Now}
}

func supportsWatchTogether(capabilities models.WatchRoomClientCapabilities) bool {
	return capabilities.NativePlayback && capabilities.StateSync && capabilities.ProtocolVersion >= protocolVersion
}

func (s *Service) Create(ctx context.Context, creatorID string, in models.WatchRoomCreate) (*models.WatchRoom, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.MediaType = strings.TrimSpace(in.MediaType)
	in.ItemID = strings.TrimSpace(in.ItemID)
	if in.Title == "" || in.MediaType == "" || in.ItemID == "" {
		return nil, ErrInvalidMedia
	}
	if _, ok := s.profiles.Get(creatorID); !ok {
		return nil, ErrNotFound
	}
	if !supportsWatchTogether(in.Capabilities) {
		return nil, ErrIncompatibleClient
	}

	invitees := make([]string, 0, len(in.InviteeProfileIDs))
	seen := map[string]bool{creatorID: true}
	for _, id := range in.InviteeProfileIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if _, ok := s.profiles.Get(id); !ok {
			continue
		}
		seen[id] = true
		invitees = append(invitees, id)
	}
	now := s.now().UTC()
	room := &models.WatchRoom{
		ID: uuid.NewString(), CreatorProfileID: creatorID, Title: in.Title,
		MediaType: in.MediaType, ItemID: in.ItemID, PosterURL: strings.TrimSpace(in.PosterURL),
		BackdropURL: strings.TrimSpace(in.BackdropURL), Params: in.Params,
		Status: models.WatchRoomStatusLobby, AnchorUpdatedAt: now, CreatedAt: now,
		ExpiresAt: now.Add(roomLifetime),
	}
	if room.Params == nil {
		room.Params = map[string]string{}
	}
	if err := s.repo.Create(ctx, room, invitees, strings.TrimSpace(in.ClientID), in.Capabilities); err != nil {
		return nil, err
	}
	return s.Get(ctx, room.ID, creatorID)
}

func (s *Service) Invitations(ctx context.Context, profileID string) ([]models.WatchRoom, error) {
	rooms, err := s.repo.ListInvitations(ctx, profileID, s.now().UTC())
	if err != nil {
		return nil, err
	}
	for i := range rooms {
		s.decorate(&rooms[i])
	}
	return rooms, nil
}

func (s *Service) Get(ctx context.Context, roomID, profileID string) (*models.WatchRoom, error) {
	allowed, err := s.repo.IsInvited(ctx, roomID, profileID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotFound
	}
	room, err := s.repo.Get(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if room == nil {
		return nil, ErrNotFound
	}
	if room.Status != models.WatchRoomStatusEnded && !room.ExpiresAt.After(s.now()) {
		if err := s.repo.EndExpired(ctx, roomID, s.now().UTC()); err != nil {
			return nil, err
		}
		room, err = s.repo.Get(ctx, roomID)
		if err != nil || room == nil {
			return room, err
		}
	}
	s.decorate(room)
	return room, nil
}

func (s *Service) Join(ctx context.Context, roomID, profileID, clientID string, capabilities models.WatchRoomClientCapabilities) (*models.WatchRoom, error) {
	if !supportsWatchTogether(capabilities) {
		return nil, ErrIncompatibleClient
	}
	room, err := s.Get(ctx, roomID, profileID)
	if err != nil {
		return nil, err
	}
	if room.Status == models.WatchRoomStatusEnded {
		return nil, ErrRoomEnded
	}
	if err := s.repo.Join(ctx, roomID, profileID, strings.TrimSpace(clientID), capabilities, s.now().UTC()); err != nil {
		return nil, err
	}
	return s.Get(ctx, roomID, profileID)
}

func (s *Service) SetReady(ctx context.Context, roomID, profileID string, ready bool) (*models.WatchRoom, error) {
	if err := s.requireMember(ctx, roomID, profileID); err != nil {
		return nil, err
	}
	if err := s.repo.SetReady(ctx, roomID, profileID, ready, s.now().UTC()); err != nil {
		return nil, err
	}
	return s.Get(ctx, roomID, profileID)
}

func (s *Service) UpdateState(ctx context.Context, roomID, profileID string, update models.WatchRoomStateUpdate) (*models.WatchRoom, error) {
	if err := s.requireMember(ctx, roomID, profileID); err != nil {
		return nil, err
	}
	switch update.Status {
	case models.WatchRoomStatusPlaying, models.WatchRoomStatusPaused:
	default:
		return nil, ErrInvalidState
	}
	if math.IsNaN(update.Position) || math.IsInf(update.Position, 0) || update.Position < 0 {
		return nil, ErrInvalidState
	}
	if math.IsNaN(update.Duration) || math.IsInf(update.Duration, 0) || update.Duration < 0 {
		return nil, ErrInvalidState
	}
	if err := s.repo.UpdateState(ctx, roomID, profileID, update.Status, update.Position, update.Duration, s.now().UTC()); err != nil {
		return nil, err
	}
	return s.Get(ctx, roomID, profileID)
}

func (s *Service) Heartbeat(ctx context.Context, roomID, profileID, clientID string, buffering bool) error {
	if err := s.requireMember(ctx, roomID, profileID); err != nil {
		return err
	}
	return s.repo.Heartbeat(ctx, roomID, profileID, strings.TrimSpace(clientID), buffering, s.now().UTC())
}

func (s *Service) Leave(ctx context.Context, roomID, profileID string) error {
	room, err := s.Get(ctx, roomID, profileID)
	if err != nil {
		return err
	}
	if room.Status == models.WatchRoomStatusEnded {
		return ErrRoomEnded
	}
	return s.repo.Leave(ctx, roomID, profileID)
}

func (s *Service) End(ctx context.Context, roomID, profileID string) error {
	room, err := s.Get(ctx, roomID, profileID)
	if err != nil {
		return err
	}
	if room.Status == models.WatchRoomStatusEnded {
		return nil
	}
	ok, err := s.repo.End(ctx, roomID, profileID, s.now().UTC())
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotCreator
	}
	return nil
}

func (s *Service) Cleanup(ctx context.Context) (ended int64, deleted int64, err error) {
	now := s.now().UTC()
	return s.repo.Sweep(ctx, now, now.Add(-disconnectGrace), now.Add(-endedAuditWindow))
}

func (s *Service) requireMember(ctx context.Context, roomID, profileID string) error {
	room, err := s.Get(ctx, roomID, profileID)
	if err != nil {
		return err
	}
	if room.Status == models.WatchRoomStatusEnded {
		return ErrRoomEnded
	}
	for _, member := range room.Members {
		if member.ProfileID == profileID && member.Joined {
			return nil
		}
	}
	return ErrNotMember
}

func (s *Service) decorate(room *models.WatchRoom) {
	now := s.now().UTC()
	if room.Status == models.WatchRoomStatusPlaying {
		room.Position += now.Sub(room.AnchorUpdatedAt).Seconds()
		if room.Duration > 0 && room.Position > room.Duration {
			room.Position = room.Duration
		}
	}
	for i := range room.Members {
		room.Members[i].Present = now.Sub(room.Members[i].LastSeenAt) <= presenceTTL
	}
}
