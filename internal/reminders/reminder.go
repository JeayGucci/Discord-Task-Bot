package reminders

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusScheduled  Status = "scheduled"
	StatusProcessing Status = "processing"
	StatusSent       Status = "sent"
	StatusCompleted  Status = "completed"
	StatusCancelled  Status = "cancelled"
	StatusFailed     Status = "failed"
)

type Reminder struct {
	ID               uuid.UUID `json:"id"`
	Title            string    `json:"title"`
	Description      string    `json:"description,omitempty"`
	CreatorID        string    `json:"creator_id"`
	GuildID          string    `json:"guild_id"`
	ChannelID        string    `json:"channel_id"`
	MentionTarget    string    `json:"mention_target"`
	DeliveryAt       time.Time `json:"delivery_at"`
	Timezone         string    `json:"timezone"`
	Status           Status    `json:"status"`
	Attempts         int       `json:"attempts"`
	LastError        string    `json:"last_error,omitempty"`
	DiscordMessageID string    `json:"discord_message_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type CreateParams struct {
	Title         string
	Description   string
	CreatorID     string
	GuildID       string
	ChannelID     string
	MentionTarget string
	DeliveryAt    time.Time
	Timezone      string
}

func (p CreateParams) Validate(now time.Time) error {
	if strings.TrimSpace(p.Title) == "" {
		return errors.New("title is required")
	}
	if len([]rune(p.Title)) > 200 {
		return errors.New("title must be at most 200 characters")
	}
	if p.CreatorID == "" || p.ChannelID == "" {
		return errors.New("creator and channel are required")
	}
	if !p.DeliveryAt.After(now) {
		return errors.New("delivery time must be in the future")
	}
	if p.Timezone == "" {
		return errors.New("timezone is required")
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return fmt.Errorf("invalid timezone: %w", err)
	}
	return nil
}

type ListFilter struct {
	CreatorID string
	GuildID   string
	From      time.Time
	To        time.Time
	Statuses  []Status
	Limit     int
}

type UpdateParams struct {
	ID         uuid.UUID
	CreatorID  string
	Title      string
	DeliveryAt time.Time
	Timezone   string
}

type Store interface {
	Create(context.Context, CreateParams) (Reminder, error)
	List(context.Context, ListFilter) ([]Reminder, error)
	Get(context.Context, uuid.UUID) (Reminder, error)
	Update(context.Context, UpdateParams) (Reminder, error)
	SetStatus(context.Context, uuid.UUID, string, Status) error
	SetTimezone(context.Context, string, string) error
	GetTimezone(context.Context, string, string) (string, error)
	ClaimDue(context.Context, time.Time, int) ([]Reminder, error)
	MarkSent(context.Context, uuid.UUID, string) error
	MarkFailed(context.Context, uuid.UUID, error, time.Time) error
	GetConversation(context.Context, string, string) (string, error)
	SaveConversation(context.Context, string, string, string, string, time.Time) error
	ResetConversation(context.Context, string, string) error
	DeleteUserData(context.Context, string) error
}

var ErrNotFound = errors.New("reminder not found")
