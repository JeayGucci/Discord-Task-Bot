package users

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("user not found")

type User struct {
	ID            uuid.UUID `json:"id"`
	DisplayName   string    `json:"display_name"`
	DiscordUserID string    `json:"discord_user_id,omitempty"`
	Timezone      string    `json:"timezone"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type CreateParams struct {
	DisplayName   string
	DiscordUserID string
	Timezone      string
}

type UpdateParams struct {
	ID            uuid.UUID
	DisplayName   string
	DiscordUserID string
	Timezone      string
}

func Validate(displayName, timezone string) error {
	if strings.TrimSpace(displayName) == "" {
		return errors.New("display name is required")
	}
	if len([]rune(displayName)) > 100 {
		return errors.New("display name must be at most 100 characters")
	}
	if timezone == "" {
		return errors.New("timezone is required")
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return err
	}
	return nil
}
