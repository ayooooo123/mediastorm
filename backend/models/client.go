package models

import "time"

// Client represents a device that connects to mediastorm.
// A single physical device (ID) may be associated with multiple profiles over time.
// UserID is the profile for this list instance (or the last-active profile on Get).
// Device-scoped settings are stored per (ID, UserID) pair.
type Client struct {
	ID            string    `json:"id"`            // Hardware/device-bound client ID from frontend
	UserID        string    `json:"userId"`        // Profile for this instance / last-active on raw device Get
	Name          string    `json:"name"`          // Admin-editable display name
	Nickname      string    `json:"nickname"`      // User-assigned name for this physical device
	DeviceName    string    `json:"deviceName"`    // OS-assigned device name, e.g. "Liam's iPhone"
	DeviceType    string    `json:"deviceType"`    // "iPhone", "iPad", "Apple TV", "Android Phone", "Android TV", etc.
	OS            string    `json:"os"`            // "iOS", "iPadOS", "tvOS", "Android"
	AppVersion    string    `json:"appVersion"`    // e.g., "1.2.1"
	LastSeenAt    time.Time `json:"lastSeenAt"`    // Last seen for this profile association (or device)
	FirstSeenAt   time.Time `json:"firstSeenAt"`   // First seen for this profile association (or device)
	FilterEnabled bool      `json:"filterEnabled"` // Whether custom filtering is enabled for this client
}

// ClientProfileAssociation records that a device has been used under a profile.
type ClientProfileAssociation struct {
	ClientID    string    `json:"clientId"`
	UserID      string    `json:"userId"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
}
