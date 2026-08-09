package models

import "time"

const (
	WatchRoomStatusLobby   = "lobby"
	WatchRoomStatusPlaying = "playing"
	WatchRoomStatusPaused  = "paused"
	WatchRoomStatusEnded   = "ended"
)

// WatchRoom is a persistent, profile-scoped invitation and synchronized
// playback session. Params contains the native details-route parameters needed
// for every participant to resolve their own stream.
type WatchRoom struct {
	ID               string            `json:"id"`
	CreatorProfileID string            `json:"creatorProfileId"`
	CreatorName      string            `json:"creatorName"`
	Title            string            `json:"title"`
	MediaType        string            `json:"mediaType"`
	ItemID           string            `json:"itemId"`
	PosterURL        string            `json:"posterUrl,omitempty"`
	BackdropURL      string            `json:"backdropUrl,omitempty"`
	Params           map[string]string `json:"params"`
	Status           string            `json:"status"`
	Position         float64           `json:"position"`
	Duration         float64           `json:"duration"`
	Revision         int64             `json:"revision"`
	UpdatedBy        string            `json:"updatedBy,omitempty"`
	AnchorUpdatedAt  time.Time         `json:"anchorUpdatedAt"`
	CreatedAt        time.Time         `json:"createdAt"`
	ExpiresAt        time.Time         `json:"expiresAt"`
	EndedAt          *time.Time        `json:"endedAt,omitempty"`
	EndReason        string            `json:"endReason,omitempty"`
	Members          []WatchRoomMember `json:"members"`
}

const (
	WatchRoomAccountInvitePending  = "pending"
	WatchRoomAccountInviteAccepted = "accepted"
	WatchRoomAccountInviteDeclined = "declined"
	WatchRoomAccountInviteRevoked  = "revoked"
)

// WatchRoomAccountInvite addresses another account without exposing its
// profiles. The recipient chooses a profile when accepting the invitation.
type WatchRoomAccountInvite struct {
	ID                string     `json:"id"`
	RoomID            string     `json:"roomId"`
	InviterAccountID  string     `json:"-"`
	InviteeAccountID  string     `json:"-"`
	InviteeUsername   string     `json:"inviteeUsername,omitempty"`
	AcceptedProfileID string     `json:"acceptedProfileId,omitempty"`
	Status            string     `json:"status"`
	CreatorName       string     `json:"creatorName,omitempty"`
	Title             string     `json:"title,omitempty"`
	MediaType         string     `json:"mediaType,omitempty"`
	ItemID            string     `json:"itemId,omitempty"`
	PosterURL         string     `json:"posterUrl,omitempty"`
	BackdropURL       string     `json:"backdropUrl,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	ExpiresAt         time.Time  `json:"expiresAt"`
	RespondedAt       *time.Time `json:"respondedAt,omitempty"`
	RevokedAt         *time.Time `json:"revokedAt,omitempty"`
}

type WatchRoomClientCapabilities struct {
	NativePlayback  bool `json:"nativePlayback"`
	StateSync       bool `json:"stateSync"`
	ProtocolVersion int  `json:"protocolVersion"`
}

type WatchRoomMember struct {
	ProfileID    string                      `json:"profileId"`
	Name         string                      `json:"name"`
	Color        string                      `json:"color,omitempty"`
	IconURL      string                      `json:"iconUrl,omitempty"`
	ClientID     string                      `json:"clientId,omitempty"`
	IsCreator    bool                        `json:"isCreator"`
	Ready        bool                        `json:"ready"`
	Buffering    bool                        `json:"buffering"`
	Joined       bool                        `json:"joined"`
	Present      bool                        `json:"present"`
	JoinedAt     time.Time                   `json:"joinedAt"`
	LastSeenAt   time.Time                   `json:"lastSeenAt"`
	Capabilities WatchRoomClientCapabilities `json:"capabilities"`
}

type WatchRoomCreate struct {
	Title             string                      `json:"title"`
	MediaType         string                      `json:"mediaType"`
	ItemID            string                      `json:"itemId"`
	PosterURL         string                      `json:"posterUrl,omitempty"`
	BackdropURL       string                      `json:"backdropUrl,omitempty"`
	Params            map[string]string           `json:"params"`
	InviteeProfileIDs []string                    `json:"inviteeProfileIds"`
	ClientID          string                      `json:"clientId,omitempty"`
	Capabilities      WatchRoomClientCapabilities `json:"capabilities"`
}

type WatchRoomStateUpdate struct {
	Status   string  `json:"status"`
	Position float64 `json:"position"`
	Duration float64 `json:"duration,omitempty"`
}
