package users

import "time"

type User struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Enabled   bool            `json:"enabled"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Xray      XrayProfile     `json:"xray"`
	Hysteria  HysteriaProfile `json:"hysteria"`
}

type XrayProfile struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id,omitempty"`
	UUID      string    `json:"uuid"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type HysteriaProfile struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id,omitempty"`
	Username     string    `json:"username"`
	Password     string    `json:"password,omitempty"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	LegacyCompat bool      `json:"-"`
}

type CreateRequest struct {
	Name            string `json:"name"`
	XrayEnabled     bool   `json:"xray_enabled"`
	HysteriaEnabled bool   `json:"hysteria_enabled"`
}
