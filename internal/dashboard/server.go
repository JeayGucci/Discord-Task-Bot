package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmantheitguy/Discord-Task-Bot/internal/channels"
	"github.com/jmantheitguy/Discord-Task-Bot/internal/ops"
	"github.com/jmantheitguy/Discord-Task-Bot/internal/reminders"
	"github.com/jmantheitguy/Discord-Task-Bot/internal/users"
	"golang.org/x/crypto/bcrypt"
)

type Pinger interface{ Ping(context.Context) error }
type SessionStore interface {
	CreateDashboardSession(context.Context, []byte, string, string, time.Time) error
	GetDashboardSession(context.Context, []byte) (string, string, time.Time, error)
	DeleteDashboardSession(context.Context, []byte) error
}
type UserStore interface {
	ListUsers(context.Context) ([]users.User, error)
	CreateUser(context.Context, users.CreateParams) (users.User, error)
	UpdateUser(context.Context, users.UpdateParams) (users.User, error)
	DeleteUser(context.Context, uuid.UUID) error
}
type ChannelProvider interface {
	ListChannels(context.Context) ([]channels.Group, error)
}

type Server struct {
	store             reminders.Store
	users             UserStore
	db                Pinger
	sessions          SessionStore
	channelProvider   ChannelProvider
	username          string
	passwordHash      []byte
	ownerID           string
	reminderChannelID string
	defaultTimezone   string
	logger            *slog.Logger
	recorder          *ops.Recorder
}

func New(store reminders.Store, db Pinger, sessions SessionStore, channelProvider ChannelProvider, username, passwordHash, password, ownerID, reminderChannelID, defaultTimezone string, logger *slog.Logger, recorder *ops.Recorder) *Server {
	hash := []byte(passwordHash)
	if len(hash) == 0 && password != "" {
		hash, _ = bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	}
	userStore, _ := store.(UserStore)
	return &Server{store: store, users: userStore, db: db, sessions: sessions, channelProvider: channelProvider, username: username, passwordHash: hash, ownerID: ownerID, reminderChannelID: reminderChannelID, defaultTimezone: defaultTimezone, logger: logger, recorder: recorder}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.auth(s.logout))
	mux.HandleFunc("GET /", s.index)
	mux.HandleFunc("GET /api/channels", s.listChannels)
	mux.HandleFunc("GET /api/reminders", s.list)
	mux.HandleFunc("POST /api/reminders", s.create)
	mux.HandleFunc("POST /api/reminders/{id}/cancel", s.auth(s.cancel))
	mux.HandleFunc("GET /api/users", s.listUsers)
	mux.HandleFunc("POST /api/users", s.auth(s.createUser))
	mux.HandleFunc("PUT /api/users/{id}", s.auth(s.updateUser))
	mux.HandleFunc("DELETE /api/users/{id}", s.auth(s.deleteUser))
	mux.HandleFunc("GET /api/admin/health", s.auth(s.adminHealth))
	mux.HandleFunc("GET /api/admin/logs", s.auth(s.adminLogs))
	mux.HandleFunc("GET /api/admin/reminder-activity", s.auth(s.adminReminderActivity))
	return requestLogger(s.logger, securityHeaders(mux))
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if s.db.Ping(ctx) != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	if s.channelProvider != nil {
		items, err := s.channelProvider.ListChannels(r.Context())
		if err == nil {
			s.recorder.SetHealth("dashboard_channel_source", "discord")
			writeJSON(w, 200, items)
			return
		}
		s.logger.Warn("list Discord channels", "error", err)
		s.recorder.Record("warn", "dashboard", "list Discord channels failed", ops.Attributes("error", err.Error()))
	}
	if strings.TrimSpace(s.reminderChannelID) == "" {
		s.recorder.SetHealth("dashboard_channel_source", "empty")
		writeJSON(w, 200, []channels.Group{})
		return
	}
	s.recorder.SetHealth("dashboard_channel_source", "configured fallback")
	writeJSON(w, 200, []channels.Group{{Name: "Configured default", Channels: []channels.Channel{{ID: s.reminderChannelID, Name: "default-reminder-channel"}}}})
}
func (s *Server) loginPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(loginHTML))
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.ParseForm() != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if len(s.passwordHash) == 0 || r.FormValue("username") != s.username || bcrypt.CompareHashAndPassword(s.passwordHash, []byte(r.FormValue("password"))) != nil {
		time.Sleep(250 * time.Millisecond)
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return
	}
	token, err := randomToken(32)
	if err != nil {
		http.Error(w, "login unavailable", 500)
		return
	}
	csrf, err := randomToken(24)
	if err != nil {
		http.Error(w, "login unavailable", 500)
		return
	}
	expires := time.Now().Add(24 * time.Hour)
	if s.sessions.CreateDashboardSession(r.Context(), hashToken(token), s.username, csrf, expires) != nil {
		http.Error(w, "login unavailable", 500)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "taskbot_session", Value: token, Path: "/", HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: 86400})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if c, e := r.Cookie("taskbot_session"); e == nil {
		_ = s.sessions.DeleteDashboardSession(r.Context(), hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "taskbot_session", Path: "/", HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	_, csrf, _, err := s.currentSession(r)
	if err != nil {
		csrf = ""
	}
	admin := "false"
	if csrf != "" {
		admin = "true"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTMLPage(csrf, admin)))
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, _ := time.Parse(time.RFC3339, q.Get("start"))
	to, _ := time.Parse(time.RFC3339, q.Get("end"))
	if q.Get("past") == "true" {
		to = time.Now()
		from = time.Time{}
	}
	creator := q.Get("creator_id")
	if creator == "" && q.Get("all") != "true" {
		creator = s.ownerID
	}
	items, err := s.store.List(r.Context(), reminders.ListFilter{CreatorID: creator, From: from, To: to, Limit: 500})
	if err != nil {
		http.Error(w, "could not load reminders", 500)
		return
	}
	type event struct {
		ID        string    `json:"id"`
		Title     string    `json:"title"`
		Start     time.Time `json:"start"`
		ClassName string    `json:"className"`
		Extended  any       `json:"extendedProps"`
	}
	events := make([]event, 0, len(items))
	for _, x := range items {
		events = append(events, event{ID: x.ID.String(), Title: x.Title, Start: x.DeliveryAt, ClassName: "status-" + string(x.Status), Extended: x})
	}
	writeJSON(w, 200, events)
}
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var in struct{ Title, Description, CreatorID, GuildID, ChannelID, MentionTarget, DeliveryAt, Timezone string }
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in) != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if in.CreatorID == "" {
		http.Error(w, "choose a linked Discord user to ping", 400)
		return
	}
	channelID := strings.TrimSpace(in.ChannelID)
	if channelID == "" {
		http.Error(w, "choose a channel for the reminder", 400)
		return
	}
	if in.MentionTarget == "" && in.CreatorID != "" {
		in.MentionTarget = "<@" + in.CreatorID + ">"
	}
	if in.Timezone == "" {
		in.Timezone = s.defaultTimezone
	}
	delivery, err := time.Parse(time.RFC3339, in.DeliveryAt)
	if err != nil {
		http.Error(w, "delivery_at must be RFC3339", 400)
		return
	}
	item, err := s.store.Create(r.Context(), reminders.CreateParams{Title: in.Title, Description: in.Description, CreatorID: in.CreatorID, GuildID: in.GuildID, ChannelID: channelID, MentionTarget: in.MentionTarget, DeliveryAt: delivery, Timezone: in.Timezone})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.logger.Info("dashboard reminder created", "reminder_id", item.ID, "title", item.Title, "creator_id", item.CreatorID, "guild_id", item.GuildID, "channel_id", channelID, "delivery_at", item.DeliveryAt)
	s.recorder.Record("info", "reminder", "dashboard reminder created", ops.Attributes("reminder_id", item.ID, "title", item.Title, "creator_id", item.CreatorID, "guild_id", item.GuildID, "channel_id", channelID, "delivery_at", item.DeliveryAt, "timezone", item.Timezone))
	writeJSON(w, 201, item)
}
func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid reminder ID", 400)
		return
	}
	item, err := s.store.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "reminder not found", 404)
		return
	}
	if s.store.SetStatus(r.Context(), id, item.CreatorID, reminders.StatusCancelled) != nil {
		http.Error(w, "reminder not found", 404)
		return
	}
	s.logger.Info("dashboard reminder cancelled", "reminder_id", id, "creator_id", item.CreatorID)
	s.recorder.Record("info", "reminder", "dashboard reminder cancelled", ops.Attributes("reminder_id", id, "creator_id", item.CreatorID, "channel_id", item.ChannelID))
	w.WriteHeader(204)
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	if s.users == nil {
		http.Error(w, "user management unavailable", 500)
		return
	}
	items, err := s.users.ListUsers(r.Context())
	if err != nil {
		http.Error(w, "could not load users", 500)
		return
	}
	writeJSON(w, 200, items)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if s.users == nil {
		http.Error(w, "user management unavailable", 500)
		return
	}
	var in struct{ DisplayName, DiscordUserID, Timezone string }
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in) != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	item, err := s.users.CreateUser(r.Context(), users.CreateParams{DisplayName: in.DisplayName, DiscordUserID: in.DiscordUserID, Timezone: in.Timezone})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.logger.Info("dashboard user created", "user_id", item.ID, "display_name", item.DisplayName, "discord_user_id", item.DiscordUserID)
	s.recorder.Record("info", "dashboard", "user created", ops.Attributes("user_id", item.ID, "display_name", item.DisplayName, "discord_user_id", item.DiscordUserID))
	writeJSON(w, 201, item)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if s.users == nil {
		http.Error(w, "user management unavailable", 500)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid user ID", 400)
		return
	}
	var in struct{ DisplayName, DiscordUserID, Timezone string }
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in) != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	item, err := s.users.UpdateUser(r.Context(), users.UpdateParams{ID: id, DisplayName: in.DisplayName, DiscordUserID: in.DiscordUserID, Timezone: in.Timezone})
	if err != nil {
		status := 400
		if errors.Is(err, users.ErrNotFound) {
			status = 404
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.logger.Info("dashboard user updated", "user_id", item.ID, "display_name", item.DisplayName, "discord_user_id", item.DiscordUserID)
	s.recorder.Record("info", "dashboard", "user updated", ops.Attributes("user_id", item.ID, "display_name", item.DisplayName, "discord_user_id", item.DiscordUserID))
	writeJSON(w, 200, item)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if s.users == nil {
		http.Error(w, "user management unavailable", 500)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid user ID", 400)
		return
	}
	if err := s.users.DeleteUser(r.Context(), id); err != nil {
		status := 500
		if errors.Is(err, users.ErrNotFound) {
			status = 404
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.logger.Info("dashboard user deleted", "user_id", id)
	s.recorder.Record("info", "dashboard", "user deleted", ops.Attributes("user_id", id))
	w.WriteHeader(204)
}

func (s *Server) adminHealth(w http.ResponseWriter, r *http.Request) {
	health := s.recorder.Health()
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		health["database"] = "unavailable"
		health["database_error"] = err.Error()
	} else {
		health["database"] = "ready"
	}
	health["default_user"] = "Jeay"
	health["default_channel"] = "general-to-do-list"
	health["dashboard_admin"] = true
	writeJSON(w, 200, health)
}

func (s *Server) adminLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.recorder.List(100))
}

func (s *Server) adminReminderActivity(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.List(r.Context(), reminders.ListFilter{From: time.Now().AddDate(0, -3, 0), Limit: 100})
	if err != nil {
		http.Error(w, "could not load reminder activity", 500)
		return
	}
	writeJSON(w, 200, items)
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, _, err := s.currentSession(r); err != nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.Error(w, "unauthorized", 401)
			} else {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			}
			return
		}
		next(w, r)
	}
}
func (s *Server) currentSession(r *http.Request) (string, string, time.Time, error) {
	c, e := r.Cookie("taskbot_session")
	if e != nil {
		return "", "", time.Time{}, e
	}
	return s.sessions.GetDashboardSession(r.Context(), hashToken(c.Value))
}
func (s *Server) validCSRF(r *http.Request) bool {
	_, csrf, _, e := s.currentSession(r)
	return e == nil && csrf != "" && r.Header.Get("X-CSRF-Token") == csrf
}
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func hashToken(v string) []byte { x := sha256.Sum256([]byte(v)); return x[:] }
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://cdn.jsdelivr.net 'unsafe-inline'; style-src 'self' 'unsafe-inline'")
		next.ServeHTTP(w, r)
	})
}
func requestLogger(l *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		l.Info("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}

func indexHTMLPage(csrf, admin string) string {
	page := strings.ReplaceAll(adminHTML, "{{CSRF}}", csrf)
	return strings.ReplaceAll(page, "{{ADMIN}}", admin)
}

const loginHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>TaskBot Login</title><style>body{font-family:system-ui;background:#f4f6fa;display:grid;place-items:center;min-height:100vh;margin:0}.card{background:white;padding:32px;border-radius:12px;box-shadow:0 3px 18px #17203320;width:min(360px,calc(100% - 48px))}input,button{box-sizing:border-box;width:100%;padding:11px;margin:7px 0;border:1px solid #ccd2df;border-radius:7px;font:inherit}button{background:#5865f2;color:white;border:0}</style></head><body><form class="card" method="post" action="/login"><h1>TaskBot</h1><p>Sign in to your private calendar.</p><input name="username" autocomplete="username" placeholder="Username" required autofocus><input name="password" type="password" autocomplete="current-password" placeholder="Password" required><button>Sign in</button></form></body></html>`
const adminHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>TaskBot Calendar</title>
<script src="https://cdn.jsdelivr.net/npm/fullcalendar@6.1.18/index.global.min.js"></script>
<style>
body{font-family:system-ui;margin:0;background:#f4f6fa;color:#172033}
header{padding:18px 28px;background:#5865f2;color:white;display:flex;justify-content:space-between;align-items:center;gap:16px}
header h1{font-size:22px;margin:0}
main{max-width:1280px;margin:24px auto;padding:0 20px}
.grid{display:grid;grid-template-columns:minmax(340px,410px) minmax(0,1fr);gap:20px;align-items:start}
.sidebar{position:sticky;top:16px}
.card{background:white;padding:18px;border-radius:8px;box-shadow:0 3px 18px #17203315;margin-bottom:20px}
.calendar-card{padding:16px}
h2{font-size:18px;margin:0 0 14px}
form{display:grid;gap:10px}
input,select{box-sizing:border-box;width:100%;font:inherit;padding:10px;border:1px solid #ccd2df;border-radius:7px}
form button,.actions button,#session-action,#toggle-past{box-sizing:border-box;font:inherit;padding:10px;border-radius:7px;background:#5865f2;color:white;border:0;cursor:pointer}
form button{width:100%}
#session-action,#toggle-past,.actions button{width:auto}
button.secondary{background:#eef1f8;color:#172033;border:1px solid #ccd2df}
button.danger{background:#c0392b}
.row{display:grid;grid-template-columns:1fr 1fr;gap:10px}
.status{min-height:24px;margin-top:8px}
.user{border-top:1px solid #e5e8f0;padding:12px 0;display:grid;gap:8px}
.user:first-child{border-top:0}
.actions{display:flex;gap:8px}
.admin-grid{display:grid;grid-template-columns:repeat(3,1fr);gap:12px}
.metric{border-top:1px solid #e5e8f0;padding:8px 0}
.metric:first-child{border-top:0}
.metric b{display:block;font-size:12px;text-transform:uppercase;color:#5d6678}
.log-list,.activity-list{display:grid;gap:8px;max-height:360px;overflow:auto}
.log,.activity{border-top:1px solid #e5e8f0;padding:8px 0;font-size:13px}
.log:first-child,.activity:first-child{border-top:0}
.muted{color:#5d6678}
.status-completed,.status-sent{opacity:.65}
.status-failed{background:#c0392b!important}
.status-cancelled{text-decoration:line-through;opacity:.5}
.fc{font-size:14px}
.fc .fc-toolbar{flex-wrap:wrap;gap:8px}
.fc .fc-toolbar-chunk{display:flex;align-items:center}
.fc .fc-daygrid-day-frame{min-height:104px}
.fc .fc-daygrid-event{white-space:normal;line-height:1.25}
@media(max-width:900px){main{margin:12px auto;padding:0 12px}.grid{grid-template-columns:1fr}.sidebar{position:static;order:2}.calendar-column{order:1}.row{grid-template-columns:1fr}.admin-grid{grid-template-columns:1fr}}
@media(max-width:600px){header{padding:14px 16px}header h1{font-size:18px}.card{padding:14px}.calendar-card{padding:10px}.fc .fc-toolbar{align-items:flex-start}.fc .fc-toolbar-title{font-size:24px}.fc .fc-button{padding:8px 10px}.fc .fc-daygrid-day-frame{min-height:82px}.fc .fc-col-header-cell-cushion,.fc .fc-daygrid-day-number{padding:6px}.fc .fc-daygrid-event{font-size:12px}}
</style>
</head>
<body>
<header><h1>TaskBot Dashboard</h1><button id="session-action">Admin login</button></header>
<main>
<div class="grid">
<section class="sidebar">
<div class="card" id="admin-users">
<h2>Managed Users</h2>
<form id="user-create">
<input name="display" placeholder="Display name" maxlength="100" required>
<input name="discord" placeholder="Discord user ID">
<input name="timezone" value="America/New_York" required>
<button>Create user</button>
</form>
<div id="users"></div>
</div>
<div class="card" id="admin-health">
<h2>Admin Health</h2>
<div id="health"></div>
</div>
<div class="card">
<h2>Create Reminder</h2>
<form id="create">
<input name="title" placeholder="Reminder title" maxlength="200" required>
<input name="delivery" type="datetime-local" required>
<select name="user" id="reminder-user" required></select>
<select name="channel" id="reminder-channel" required></select>
<input name="timezone" id="reminder-timezone" value="America/New_York" required>
<button>Create reminder</button>
</form>
<div id="status" class="status"></div>
<button id="toggle-past" class="secondary" type="button">Show past reminders</button>
</div>
</section>
<section class="calendar-column">
<div class="card calendar-card"><div id="calendar"></div></div>
<div class="card" id="admin-activity">
<h2>Reminder Activity</h2>
<div id="activity" class="activity-list"></div>
</div>
<div class="card" id="admin-logs">
<h2>Admin Logs</h2>
<div id="logs" class="log-list"></div>
</div>
</section>
</div>
</main>
<script>
const csrf='{{CSRF}}';
const isAdmin={{ADMIN}};
const defaultReminderUser='jeay';
const defaultReminderChannel='general-to-do-list';
let users=[];
let channelGroups=[];
let calendar;
let showPast=false;
let reminderUserInitialized=false;
const compactCalendar=()=>window.matchMedia('(max-width: 640px)').matches;
const api=(p,o={})=>{o.headers={...(o.headers||{}),'X-CSRF-Token':csrf};return fetch(p,o)};
const status=document.getElementById('status');
function esc(s){return String(s||'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))}
function selectedUser(){return users.find(u=>u.id===document.getElementById('reminder-user').value)}
function refreshReminderUser(){
 const select=document.getElementById('reminder-user');
 const previous=select.value;
 select.innerHTML='<option value="">Choose user to ping</option>'+users.filter(u=>u.discord_user_id).map(u=>'<option value="'+esc(u.id)+'">'+esc(u.display_name)+' ('+esc(u.discord_user_id)+')</option>').join('');
 if(previous&&[...select.options].some(o=>o.value===previous)){
  select.value=previous;
 }else if(!reminderUserInitialized){
  const jeay=users.find(u=>u.discord_user_id&&String(u.display_name||'').trim().toLowerCase()===defaultReminderUser);
  if(jeay)select.value=jeay.id;
 }
 reminderUserInitialized=true;
 const u=selectedUser();
 if(u)document.getElementById('reminder-timezone').value=u.timezone;
}
async function loadUsers(){
 const r=await api('/api/users');
 if(!r.ok)throw Error(await r.text());
 users=await r.json();
 renderUsers();
 refreshReminderUser();
 if(calendar)calendar.refetchEvents();
}
async function loadChannels(){
 const r=await api('/api/channels');
 if(!r.ok)throw Error(await r.text());
 channelGroups=await r.json();
 renderChannels();
 if(calendar)calendar.refetchEvents();
}
function renderChannels(){
 const select=document.getElementById('reminder-channel');
 const previous=select.value;
 select.innerHTML='<option value="">Choose channel</option>'+channelGroups.map(g=>'<optgroup label="'+esc(g.name)+'">'+(g.channels||[]).map(c=>'<option value="'+esc(c.id)+'">#'+esc(c.name)+'</option>').join('')+'</optgroup>').join('');
 const options=[...select.options];
 if(previous&&options.some(o=>o.value===previous)){
  select.value=previous;
  return;
 }
 const general=options.find(o=>String(o.textContent||'').replace(/^#/,'').trim().toLowerCase()===defaultReminderChannel);
 if(general)select.value=general.value;
}
function renderUsers(){
 const box=document.getElementById('users');
 box.innerHTML=users.map(u=>'<div class="user" data-id="'+esc(u.id)+'">'+
  '<input name="display" value="'+esc(u.display_name)+'" maxlength="100">'+
  '<input name="discord" value="'+esc(u.discord_user_id)+'" placeholder="Discord user ID">'+
  '<input name="timezone" value="'+esc(u.timezone)+'">'+
  '<div class="actions"><button class="secondary" data-action="save" type="button">Save</button><button class="danger" data-action="delete" type="button">Delete</button></div>'+
  '</div>').join('');
}
function renderHealth(data){
 const box=document.getElementById('health');
 box.innerHTML=Object.keys(data).sort().map(k=>'<div class="metric"><b>'+esc(k.replaceAll('_',' '))+'</b><span>'+esc(data[k])+'</span></div>').join('');
}
function renderLogs(items){
 document.getElementById('logs').innerHTML=(items||[]).map(x=>'<div class="log"><b>'+esc(x.level)+' '+esc(x.source)+'</b> <span class="muted">'+esc(new Date(x.time).toLocaleString())+'</span><br>'+esc(x.message)+(x.attributes?' <span class="muted">'+esc(JSON.stringify(x.attributes))+'</span>':'')+'</div>').join('');
}
function renderActivity(items){
 document.getElementById('activity').innerHTML=(items||[]).map(r=>'<div class="activity"><b>'+esc(r.status)+'</b> <span class="muted">'+esc(new Date(r.delivery_at).toLocaleString())+'</span><br>'+esc(r.title)+' for <code>'+esc(r.creator_id)+'</code> in <code>#'+esc(channelName(r.channel_id))+'</code></div>').join('');
}
function channelName(id){
 for(const group of channelGroups){for(const channel of (group.channels||[])){if(channel.id===id)return channel.name}}
 return id||'unknown';
}
async function loadAdmin(){
 if(!isAdmin)return;
 const [health,logs,activity]=await Promise.all([api('/api/admin/health').then(r=>r.json()),api('/api/admin/logs').then(r=>r.json()),api('/api/admin/reminder-activity').then(r=>r.json())]);
 renderHealth(health);
 renderLogs(logs);
 renderActivity(activity);
}
document.addEventListener('DOMContentLoaded',()=>{
 calendar=new FullCalendar.Calendar(document.getElementById('calendar'),{initialView:'dayGridMonth',height:'auto',expandRows:true,aspectRatio:compactCalendar()?0.72:1.45,dayMaxEventRows:compactCalendar()?2:3,customButtons:{prevText:{text:'<',click:()=>calendar.prev()},nextText:{text:'>',click:()=>calendar.next()}},headerToolbar:{left:'prevText,nextText today',center:'title',right:'dayGridMonth,timeGridWeek,listMonth'},events:(i,ok,fail)=>{
  api('/api/reminders?start='+encodeURIComponent(i.startStr)+'&end='+encodeURIComponent(i.endStr)+'&all=true&past='+showPast).then(r=>r.ok?r.json():Promise.reject(Error('Unable to load reminders'))).then(ok).catch(fail)
 },eventContent:i=>{const r=i.event.extendedProps;return {html:'<b>'+esc(i.event.title)+'</b><br><span>'+esc(r.creator_id||'')+' · #'+esc(channelName(r.channel_id||''))+'</span>'}},eventClick:i=>{if(confirm('Cancel '+i.event.title+'?'))api('/api/reminders/'+i.event.id+'/cancel',{method:'POST'}).then(r=>{if(!r.ok)throw Error('Cancel failed');calendar.refetchEvents();return loadAdmin()}).catch(e=>status.textContent=e.message)}});
 calendar.render();
 if(!isAdmin){document.getElementById('admin-users').hidden=true;document.getElementById('admin-health').hidden=true;document.getElementById('admin-activity').hidden=true;document.getElementById('admin-logs').hidden=true}
 const sessionAction=document.getElementById('session-action');
 sessionAction.textContent=isAdmin?'Sign out':'Admin login';
 sessionAction.onclick=()=>isAdmin?api('/logout',{method:'POST'}).then(()=>location='/'):location='/login';
 document.getElementById('reminder-user').onchange=()=>{const u=selectedUser();if(u)document.getElementById('reminder-timezone').value=u.timezone};
 document.getElementById('toggle-past').onclick=e=>{showPast=!showPast;e.target.textContent=showPast?'Show current reminders':'Show past reminders';calendar.changeView(showPast?'listMonth':'dayGridMonth');calendar.refetchEvents()};
 document.getElementById('user-create').onsubmit=e=>{e.preventDefault();const f=new FormData(e.target);api('/api/users',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({DisplayName:f.get('display'),DiscordUserID:f.get('discord'),Timezone:f.get('timezone')})}).then(async r=>{if(!r.ok)throw Error(await r.text());e.target.reset();e.target.elements.timezone.value='America/New_York';return loadUsers()}).catch(e=>status.textContent=e.message)};
 document.getElementById('users').onclick=e=>{const btn=e.target.closest('button');if(!btn)return;const row=btn.closest('.user');const id=row.dataset.id;if(btn.dataset.action==='delete'){if(!confirm('Delete this user?'))return;api('/api/users/'+id,{method:'DELETE'}).then(async r=>{if(!r.ok)throw Error(await r.text());return loadUsers()}).catch(e=>status.textContent=e.message);return}const body={DisplayName:row.querySelector('[name=display]').value,DiscordUserID:row.querySelector('[name=discord]').value,Timezone:row.querySelector('[name=timezone]').value};api('/api/users/'+id,{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)}).then(async r=>{if(!r.ok)throw Error(await r.text());return loadUsers()}).catch(e=>status.textContent=e.message)};
 document.getElementById('create').onsubmit=e=>{e.preventDefault();const f=new FormData(e.target);const u=selectedUser();if(!u||!u.discord_user_id){status.textContent='Choose a linked Discord user to ping.';return}if(!f.get('channel')){status.textContent='Choose a channel.';return}api('/api/reminders',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({Title:f.get('title'),CreatorID:u.discord_user_id,ChannelID:f.get('channel'),DeliveryAt:new Date(f.get('delivery')).toISOString(),Timezone:f.get('timezone')})}).then(async r=>{if(!r.ok)throw Error(await r.text());status.textContent='Reminder created.';e.target.elements.title.value='';calendar.refetchEvents();return loadAdmin()}).catch(e=>status.textContent=e.message)};
 window.addEventListener('resize',()=>{calendar.setOption('aspectRatio',compactCalendar()?0.72:1.45);calendar.setOption('dayMaxEventRows',compactCalendar()?2:3);calendar.updateSize()});
 Promise.all([loadUsers(),loadChannels()]).then(loadAdmin).catch(e=>status.textContent=e.message);
});
</script>
</body>
</html>`
