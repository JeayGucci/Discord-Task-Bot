package dashboard

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmantheitguy/Discord-Task-Bot/internal/reminders"
)

type fakeStore struct{}

func (fakeStore) Create(context.Context, reminders.CreateParams) (reminders.Reminder, error) {
	return reminders.Reminder{}, errors.New("unused")
}
func (fakeStore) List(context.Context, reminders.ListFilter) ([]reminders.Reminder, error) {
	return []reminders.Reminder{}, nil
}
func (fakeStore) Get(context.Context, uuid.UUID) (reminders.Reminder, error) {
	return reminders.Reminder{}, errors.New("unused")
}
func (fakeStore) Update(context.Context, reminders.UpdateParams) (reminders.Reminder, error) {
	return reminders.Reminder{}, errors.New("unused")
}
func (fakeStore) SetStatus(context.Context, uuid.UUID, string, reminders.Status) error { return nil }
func (fakeStore) SetTimezone(context.Context, string, string) error                    { return nil }
func (fakeStore) GetTimezone(context.Context, string, string) (string, error)          { return "UTC", nil }
func (fakeStore) ClaimDue(context.Context, time.Time, int) ([]reminders.Reminder, error) {
	return nil, nil
}
func (fakeStore) MarkSent(context.Context, uuid.UUID, string) error               { return nil }
func (fakeStore) MarkFailed(context.Context, uuid.UUID, error, time.Time) error   { return nil }
func (fakeStore) GetConversation(context.Context, string, string) (string, error) { return "", nil }
func (fakeStore) SaveConversation(context.Context, string, string, string, string, time.Time) error {
	return nil
}
func (fakeStore) ResetConversation(context.Context, string, string) error { return nil }
func (fakeStore) DeleteUserData(context.Context, string) error            { return nil }
func (fakeStore) Ping(context.Context) error                              { return nil }

type fakeSessions struct {
	hash           []byte
	username, csrf string
	expires        time.Time
}

func (f *fakeSessions) CreateDashboardSession(_ context.Context, h []byte, u, c string, e time.Time) error {
	f.hash = append([]byte(nil), h...)
	f.username = u
	f.csrf = c
	f.expires = e
	return nil
}
func (f *fakeSessions) GetDashboardSession(_ context.Context, h []byte) (string, string, time.Time, error) {
	if string(h) != string(f.hash) || time.Now().After(f.expires) {
		return "", "", time.Time{}, errors.New("not found")
	}
	return f.username, f.csrf, f.expires, nil
}
func (f *fakeSessions) DeleteDashboardSession(context.Context, []byte) error { return nil }

func TestLoginAndAuthenticatedAPI(t *testing.T) {
	sessions := &fakeSessions{}
	s := New(fakeStore{}, fakeStore{}, sessions, "admin", "", "correct horse", "owner", "UTC", slog.Default())
	handler := s.Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/reminders", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", rec.Code)
	}
	form := url.Values{"username": {"admin"}, "password": {"correct horse"}}
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login status=%d body=%s", rec.Code, rec.Body.String())
	}
	result := rec.Result()
	cookies := result.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatal("secure session cookie not set")
	}
	req = httptest.NewRequest(http.MethodGet, "/api/reminders", nil)
	req.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated status=%d", rec.Code)
	}
}

func TestInvalidLogin(t *testing.T) {
	s := New(fakeStore{}, fakeStore{}, &fakeSessions{}, "admin", "", "right", "owner", "UTC", slog.Default())
	form := url.Values{"username": {"admin"}, "password": {"wrong"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rec.Code)
	}
}
