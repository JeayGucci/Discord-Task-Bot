package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/JeayGucci/Discord-Task-Bot/internal/ops"
	"github.com/JeayGucci/Discord-Task-Bot/internal/reminders"
)

type fakeStore struct {
	due    []reminders.Reminder
	sent   uuid.UUID
	failed uuid.UUID
}

func (f *fakeStore) ClaimDue(context.Context, time.Time, int) ([]reminders.Reminder, error) {
	return f.due, nil
}
func (f *fakeStore) MarkSent(_ context.Context, id uuid.UUID, _ string) error {
	f.sent = id
	return nil
}
func (f *fakeStore) MarkFailed(_ context.Context, id uuid.UUID, _ error, _ time.Time) error {
	f.failed = id
	return nil
}

type fakeSender struct{ err error }

func (f fakeSender) SendReminder(context.Context, reminders.Reminder) (string, error) {
	return "message", f.err
}

func TestRunOnceMarksSent(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{due: []reminders.Reminder{{ID: id}}}
	s := New(store, fakeSender{}, time.Second, 1, slog.New(slog.NewTextHandler(io.Discard, nil)), ops.NewRecorder(10))
	s.runOnce(context.Background())
	if store.sent != id {
		t.Fatalf("sent=%s", store.sent)
	}
}

func TestRunOnceMarksFailed(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{due: []reminders.Reminder{{ID: id, Attempts: 1}}}
	s := New(store, fakeSender{err: errors.New("nope")}, time.Second, 1, slog.New(slog.NewTextHandler(io.Discard, nil)), ops.NewRecorder(10))
	s.runOnce(context.Background())
	if store.failed != id {
		t.Fatalf("failed=%s", store.failed)
	}
}
