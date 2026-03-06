package models

import "time"

// CustomList represents a user-created list of saved titles.
type CustomList struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	ItemCount int       `json:"itemCount,omitempty"`
}
