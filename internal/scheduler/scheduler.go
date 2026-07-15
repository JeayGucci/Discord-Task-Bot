package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
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
}

func New(store Store, sender Sender, interval time.Duration, limit int, logger *slog.Logger) *Scheduler {
	return &Scheduler{store: store, sender: sender, interval: interval, limit: limit, logger: logger}
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
	due, err := s.store.ClaimDue(ctx, time.Now(), s.limit)
	if err != nil {
		s.logger.Error("claim due reminders", "error", err)
		return
	}
	for _, reminder := range due {
		messageID, err := s.sender.SendReminder(ctx, reminder)
		if err == nil {
			if err := s.store.MarkSent(ctx, reminder.ID, messageID); err != nil {
				s.logger.Error("mark reminder sent", "id", reminder.ID, "error", err)
			}
			continue
		}
		minutes := math.Pow(2, float64(reminder.Attempts))
		if minutes > 60 {
			minutes = 60
		}
		retryAt := time.Now().Add(time.Duration(minutes) * time.Minute)
		if markErr := s.store.MarkFailed(ctx, reminder.ID, fmt.Errorf("discord delivery: %w", err), retryAt); markErr != nil {
			s.logger.Error("mark reminder failed", "id", reminder.ID, "error", markErr)
		}
	}
}
