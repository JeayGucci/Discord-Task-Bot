package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jmantheitguy/Discord-Task-Bot/internal/ops"
	"github.com/jmantheitguy/Discord-Task-Bot/internal/reminders"
)

type Sender interface {
	SendReminder(context.Context, reminders.Reminder) (string, error)
}

type Store interface {
	ClaimDue(context.Context, time.Time, int) ([]reminders.Reminder, error)
	MarkSent(context.Context, uuid.UUID, string) error
	MarkFailed(context.Context, uuid.UUID, error, time.Time) error
}

type Scheduler struct {
	store    Store
	sender   Sender
	interval time.Duration
	limit    int
	logger   *slog.Logger
	recorder *ops.Recorder
}

func New(store Store, sender Sender, interval time.Duration, limit int, logger *slog.Logger, recorder *ops.Recorder) *Scheduler {
	return &Scheduler{store: store, sender: sender, interval: interval, limit: limit, logger: logger, recorder: recorder}
}

func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	s.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runOnce(ctx)
		}
	}
}

func (s *Scheduler) runOnce(ctx context.Context) {
	s.recorder.LastSchedulerTick(ctx)
	due, err := s.store.ClaimDue(ctx, time.Now(), s.limit)
	if err != nil {
		s.logger.Error("claim due reminders", "error", err)
		s.recorder.Record("error", "scheduler", "claim due reminders failed", ops.Attributes("error", err.Error()))
		return
	}
	for _, reminder := range due {
		s.logger.Info(
			"delivering reminder",
			"reminder_id", reminder.ID,
			"title", reminder.Title,
			"creator_id", reminder.CreatorID,
			"guild_id", reminder.GuildID,
			"channel_id", reminder.ChannelID,
			"delivery_at", reminder.DeliveryAt,
			"attempt", reminder.Attempts,
		)
		messageID, err := s.sender.SendReminder(ctx, reminder)
		if err == nil {
			if err := s.store.MarkSent(ctx, reminder.ID, messageID); err != nil {
				s.logger.Error("mark reminder sent", "id", reminder.ID, "error", err)
			}
			s.logger.Info(
				"reminder delivered",
				"reminder_id", reminder.ID,
				"creator_id", reminder.CreatorID,
				"guild_id", reminder.GuildID,
				"channel_id", reminder.ChannelID,
				"discord_message_id", messageID,
			)
			s.recorder.Record("info", "reminder", "reminder delivered", ops.Attributes("reminder_id", reminder.ID, "creator_id", reminder.CreatorID, "guild_id", reminder.GuildID, "channel_id", reminder.ChannelID, "discord_message_id", messageID))
			continue
		}
		minutes := math.Pow(2, float64(reminder.Attempts))
		if minutes > 60 {
			minutes = 60
		}
		retryAt := time.Now().Add(time.Duration(minutes) * time.Minute)
		s.logger.Warn(
			"reminder delivery failed",
			"reminder_id", reminder.ID,
			"creator_id", reminder.CreatorID,
			"guild_id", reminder.GuildID,
			"channel_id", reminder.ChannelID,
			"retry_at", retryAt,
			"error", err,
		)
		s.recorder.Record("warn", "reminder", "reminder delivery failed", ops.Attributes("reminder_id", reminder.ID, "creator_id", reminder.CreatorID, "guild_id", reminder.GuildID, "channel_id", reminder.ChannelID, "retry_at", retryAt, "error", err.Error()))
		if markErr := s.store.MarkFailed(ctx, reminder.ID, fmt.Errorf("discord delivery: %w", err), retryAt); markErr != nil {
			s.logger.Error("mark reminder failed", "id", reminder.ID, "error", markErr)
			s.recorder.Record("error", "scheduler", "mark reminder failed failed", ops.Attributes("reminder_id", reminder.ID, "error", markErr.Error()))
		}
	}
}
