package models

import "time"

// ProfileUnlock is a server-issued grant proving that a PIN-protected profile
// was unlocked by its PIN within a specific account session. Only hashes of the
// unlock token and of the session token are stored.
type ProfileUnlock struct {
	TokenHash      string `json:"tokenHash"`
	SessionHash    string `json:"sessionHash"`
	AccountID      string `json:"accountId"`
	ProfileID      string `json:"profileId"`
	PinFingerprint string `json:"pinFingerprint"`
	// SessionBound grants have no lifetime of their own: they live as long as
	// the session lineage and follow its expiry across refresh rotations.
	SessionBound bool      `json:"sessionBound"`
	ExpiresAt    time.Time `json:"expiresAt"`
	CreatedAt    time.Time `json:"createdAt"`
}
