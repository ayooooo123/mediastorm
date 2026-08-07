package models

import "strings"

// ClientSettingsKeySep separates clientID and userID in composite settings map keys.
// The unit separator is extremely unlikely to appear in either ID.
const ClientSettingsKeySep = "\x1f"

// ClientSettingsKey builds the composite map key for person×device settings.
func ClientSettingsKey(clientID, userID string) string {
	return strings.TrimSpace(clientID) + ClientSettingsKeySep + strings.TrimSpace(userID)
}

// SplitClientSettingsKey parses a composite settings key.
// Legacy keys (device-only, no separator) return userID empty and ok=false.
func SplitClientSettingsKey(key string) (clientID, userID string, ok bool) {
	clientID, userID, found := strings.Cut(key, ClientSettingsKeySep)
	if !found || clientID == "" || userID == "" {
		return key, "", false
	}
	return clientID, userID, true
}
