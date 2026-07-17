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
	"github.com/jmantheitguy/Discord-Task-Bot/internal/users"
)

type fakeStore struct {
	lastList reminders.ListFilter
}

func (fakeStore) Create(context.Context, reminders.CreateParams) (reminders.Reminder, error) {
	return reminders.Reminder{ID: uuid.New(), Title: "Created", CreatorID: "target", ChannelID: "fixed", DeliveryAt: time.Now().Add(time.Hour), Timezone: "UTC", Status: reminders.StatusScheduled}, nil
}
func (f *fakeStore) List(_ context.Context, filter reminders.ListFilter) ([]reminders.Reminder, error) {
	f.lastList = filter
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
func (fakeStore) ListUsers(context.Context) ([]users.User, error) {
	return []users.User{{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), DisplayName: "Owner", DiscordUserID: "owner", Timezone: "UTC"}}, nil
}
func (fakeStore) CreateUser(context.Context, users.CreateParams) (users.User, error) {
	return users.User{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), DisplayName: "New", DiscordUserID: "123", Timezone: "UTC"}, nil
}
func (fakeStore) UpdateUser(context.Context, users.UpdateParams) (users.User, error) {
	return users.User{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), DisplayName: "Updated", DiscordUserID: "123", Timezone: "UTC"}, nil
}
func (fakeStore) DeleteUser(context.Context, uuid.UUID) error { return nil }

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
	store := &fakeStore{}
	s := New(store, store, sessions, "admin", "", "correct horse", "owner", "fixed-channel", "UTC", slog.Default())
	handler := s.Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/reminders", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("public reminders status=%d", rec.Code)
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
	store := &fakeStore{}
	s := New(store, store, &fakeSessions{}, "admin", "", "right", "owner", "fixed-channel", "UTC", slog.Default())
	form := url.Values{"username": {"admin"}, "password": {"wrong"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestAuthenticatedUserAPI(t *testing.T) {
	sessions := &fakeSessions{hash: hashToken("token"), username: "admin", csrf: "csrf", expires: time.Now().Add(time.Hour)}
	store := &fakeStore{}
	s := New(store, store, sessions, "admin", "", "right", "owner", "fixed-channel", "UTC", slog.Default())
	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	req.AddCookie(&http.Cookie{Name: "taskbot_session", Value: "token"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list users status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(`{"DisplayName":"New","DiscordUserID":"123","Timezone":"UTC"}`))
	req.AddCookie(&http.Cookie{Name: "taskbot_session", Value: "token"})
	req.Header.Set("X-CSRF-Token", "csrf")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create user status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPublicReminderCreate(t *testing.T) {
	store := &fakeStore{}
	s := New(store, store, &fakeSessions{}, "admin", "", "right", "owner", "fixed-channel", "UTC", slog.Default())
	req := httptest.NewRequest(http.MethodPost, "/api/reminders", strings.NewReader(`{"Title":"Public","CreatorID":"target","DeliveryAt":"2030-01-01T12:00:00Z","Timezone":"UTC"}`))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("public create status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPublicReminderListCanShowAllUsers(t *testing.T) {
	store := &fakeStore{}
	s := New(store, store, &fakeSessions{}, "admin", "", "right", "owner", "fixed-channel", "UTC", slog.Default())
	req := httptest.NewRequest(http.MethodGet, "/api/reminders?all=true&start=2030-01-01T00:00:00Z&end=2030-02-01T00:00:00Z", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.lastList.CreatorID != "" {
		t.Fatalf("creator=%q, want empty all-user filter", store.lastList.CreatorID)
	}
}
