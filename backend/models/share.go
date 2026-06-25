package models

import "time"

// ShareLinkMaxUses is the hard cap for every share link, including legacy rows
// that stored 0 for an earlier "unlimited" behavior.
const ShareLinkMaxUses = 5

// ShareLink is a shareable playback link. It captures the playback parameters at
// share time; opening it mints a short-lived stream-scoped session. A link is
// valid until it expires, is deactivated, or reaches its use cap (MaxUses).
type ShareLink struct {
	Token      string            `json:"token"`
	AccountID  string            `json:"accountId"`
	IsMaster   bool              `json:"-"`
	Label      string            `json:"label,omitempty"`
	Params     map[string]string `json:"-"`
	MaxUses    int               `json:"maxUses"` // hard-capped use limit; legacy 0 resolves to ShareLinkMaxUses
	UseCount   int               `json:"useCount"`
	Active     bool              `json:"active"`
	CreatedAt  time.Time         `json:"createdAt"`
	ExpiresAt  time.Time         `json:"expiresAt"`
	LastUsedAt *time.Time        `json:"lastUsedAt,omitempty"`
}

// Expired reports whether the link is past its expiry.
func (s *ShareLink) Expired() bool { return time.Now().After(s.ExpiresAt) }

// EffectiveMaxUses returns the enforced use cap for this link.
func (s *ShareLink) EffectiveMaxUses() int {
	if s.MaxUses <= 0 || s.MaxUses > ShareLinkMaxUses {
		return ShareLinkMaxUses
	}
	return s.MaxUses
}

// Exhausted reports whether a capped link has reached its use limit.
func (s *ShareLink) Exhausted() bool { return s.UseCount >= s.EffectiveMaxUses() }

// Usable reports whether the link can currently mint a playback session.
func (s *ShareLink) Usable() bool { return s.Active && !s.Expired() && !s.Exhausted() }
