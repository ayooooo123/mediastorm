package models

import "time"

// HistorySyncSnapshot is the set of normalized remote history keys a
// bidirectional history sync last confirmed on a provider. SyncedAt is when
// local and remote were last reconciled; local watches after it are re-watches.
type HistorySyncSnapshot struct {
	Key      string
	Keys     []string
	SyncedAt time.Time
}
