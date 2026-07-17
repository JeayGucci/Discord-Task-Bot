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

type Server struct {
	store             reminders.Store
	users             UserStore
	db                Pinger
	sessions          SessionStore
	username          string
	passwordHash      []byte
	ownerID           string
	reminderChannelID string
	defaultTimezone   string
	logger            *slog.Logger
}

func New(store reminders.Store, db Pinger, sessions SessionStore, username, passwordHash, password, ownerID, reminderChannelID, defaultTimezone string, logger *slog.Logger) *Server {
	hash := []byte(passwordHash)
	if len(hash) == 0 && password != "" {
		hash, _ = bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	}
	userStore, _ := store.(UserStore)
	return &Server{store: store, users: userStore, db: db, sessions: sessions, username: username, passwordHash: hash, ownerID: ownerID, reminderChannelID: reminderChannelID, defaultTimezone: defaultTimezone, logger: logger}
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
	mux.HandleFunc("GET /api/reminders", s.list)
	mux.HandleFunc("POST /api/reminders", s.create)
	mux.HandleFunc("POST /api/reminders/{id}/cancel", s.auth(s.cancel))
	mux.HandleFunc("GET /api/users", s.listUsers)
	mux.HandleFunc("POST /api/users", s.auth(s.createUser))
	mux.HandleFunc("PUT /api/users/{id}", s.auth(s.updateUser))
	mux.HandleFunc("DELETE /api/users/{id}", s.auth(s.deleteUser))
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
	if creator == "" {
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
	var in struct{ Title, Description, CreatorID, GuildID, MentionTarget, DeliveryAt, Timezone string }
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in) != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if in.CreatorID == "" {
		http.Error(w, "choose a linked Discord user to ping", 400)
		return
	}
	channelID := strings.TrimSpace(s.reminderChannelID)
	if channelID == "" {
		http.Error(w, "reminder channel is not configured", 500)
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
	w.WriteHeader(204)
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
main{max-width:1180px;margin:24px auto;padding:0 20px}
.grid{display:grid;grid-template-columns:380px 1fr;gap:20px;align-items:start}
.card{background:white;padding:18px;border-radius:8px;box-shadow:0 3px 18px #17203315;margin-bottom:20px}
h2{font-size:18px;margin:0 0 14px}
form{display:grid;gap:10px}
input,select,button{box-sizing:border-box;width:100%;font:inherit;padding:10px;border:1px solid #ccd2df;border-radius:7px}
button{background:#5865f2;color:white;border:0;cursor:pointer}
button.secondary{background:#eef1f8;color:#172033;border:1px solid #ccd2df}
button.danger{background:#c0392b}
.row{display:grid;grid-template-columns:1fr 1fr;gap:10px}
.status{min-height:24px;margin-top:8px}
.user{border-top:1px solid #e5e8f0;padding:12px 0;display:grid;gap:8px}
.user:first-child{border-top:0}
.actions{display:flex;gap:8px}
.status-completed,.status-sent{opacity:.65}
.status-failed{background:#c0392b!important}
.status-cancelled{text-decoration:line-through;opacity:.5}
@media(max-width:900px){.grid{grid-template-columns:1fr}.row{grid-template-columns:1fr}}
</style>
</head>
<body>
<header><h1>TaskBot Dashboard</h1><button id="session-action">Admin login</button></header>
<main>
<div class="grid">
<section>
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
<div class="card">
<h2>Create Reminder</h2>
<form id="create">
<input name="title" placeholder="Reminder title" maxlength="200" required>
<input name="delivery" type="datetime-local" required>
<select name="user" id="reminder-user" required></select>
<input name="timezone" id="reminder-timezone" value="America/New_York" required>
<button>Create reminder</button>
</form>
<div id="status" class="status"></div>
<button id="toggle-past" class="secondary" type="button">Show past reminders</button>
</div>
</section>
<section class="card"><div id="calendar"></div></section>
</div>
</main>
<script>
const csrf='{{CSRF}}';
const isAdmin={{ADMIN}};
let users=[];
let calendar;
let showPast=false;
const api=(p,o={})=>{o.headers={...(o.headers||{}),'X-CSRF-Token':csrf};return fetch(p,o)};
const status=document.getElementById('status');
function esc(s){return String(s||'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))}
function selectedUser(){return users.find(u=>u.id===document.getElementById('reminder-user').value)}
function refreshReminderUser(){
 const select=document.getElementById('reminder-user');
 const previous=select.value;
 select.innerHTML='<option value="">Choose user to ping</option>'+users.filter(u=>u.discord_user_id).map(u=>'<option value="'+esc(u.id)+'">'+esc(u.display_name)+' ('+esc(u.discord_user_id)+')</option>').join('');
 if([...select.options].some(o=>o.value===previous))select.value=previous;
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
function renderUsers(){
 const box=document.getElementById('users');
 box.innerHTML=users.map(u=>'<div class="user" data-id="'+esc(u.id)+'">'+
  '<input name="display" value="'+esc(u.display_name)+'" maxlength="100">'+
  '<input name="discord" value="'+esc(u.discord_user_id)+'" placeholder="Discord user ID">'+
  '<input name="timezone" value="'+esc(u.timezone)+'">'+
  '<div class="actions"><button class="secondary" data-action="save" type="button">Save</button><button class="danger" data-action="delete" type="button">Delete</button></div>'+
  '</div>').join('');
}
document.addEventListener('DOMContentLoaded',()=>{
 calendar=new FullCalendar.Calendar(document.getElementById('calendar'),{initialView:'dayGridMonth',headerToolbar:{left:'prev,next today',center:'title',right:'dayGridMonth,timeGridWeek,listMonth'},events:(i,ok,fail)=>{
  const u=selectedUser();
  const creator=u&&u.discord_user_id?u.discord_user_id:'';
  if(!creator){ok([]);return}
  api('/api/reminders?start='+encodeURIComponent(i.startStr)+'&end='+encodeURIComponent(i.endStr)+'&creator_id='+encodeURIComponent(creator)+'&past='+showPast).then(r=>r.ok?r.json():Promise.reject(Error('Unable to load reminders'))).then(ok).catch(fail)
 },eventClick:i=>{if(confirm('Cancel '+i.event.title+'?'))api('/api/reminders/'+i.event.id+'/cancel',{method:'POST'}).then(r=>{if(!r.ok)throw Error('Cancel failed');calendar.refetchEvents()}).catch(e=>status.textContent=e.message)}});
 calendar.render();
 if(!isAdmin)document.getElementById('admin-users').hidden=true;
 const sessionAction=document.getElementById('session-action');
 sessionAction.textContent=isAdmin?'Sign out':'Admin login';
 sessionAction.onclick=()=>isAdmin?api('/logout',{method:'POST'}).then(()=>location='/'):location='/login';
 document.getElementById('reminder-user').onchange=()=>{const u=selectedUser();if(u)document.getElementById('reminder-timezone').value=u.timezone;calendar.refetchEvents()};
 document.getElementById('toggle-past').onclick=e=>{showPast=!showPast;e.target.textContent=showPast?'Show current reminders':'Show past reminders';calendar.changeView(showPast?'listMonth':'dayGridMonth');calendar.refetchEvents()};
 document.getElementById('user-create').onsubmit=e=>{e.preventDefault();const f=new FormData(e.target);api('/api/users',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({DisplayName:f.get('display'),DiscordUserID:f.get('discord'),Timezone:f.get('timezone')})}).then(async r=>{if(!r.ok)throw Error(await r.text());e.target.reset();e.target.elements.timezone.value='America/New_York';return loadUsers()}).catch(e=>status.textContent=e.message)};
 document.getElementById('users').onclick=e=>{const btn=e.target.closest('button');if(!btn)return;const row=btn.closest('.user');const id=row.dataset.id;if(btn.dataset.action==='delete'){if(!confirm('Delete this user?'))return;api('/api/users/'+id,{method:'DELETE'}).then(async r=>{if(!r.ok)throw Error(await r.text());return loadUsers()}).catch(e=>status.textContent=e.message);return}const body={DisplayName:row.querySelector('[name=display]').value,DiscordUserID:row.querySelector('[name=discord]').value,Timezone:row.querySelector('[name=timezone]').value};api('/api/users/'+id,{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)}).then(async r=>{if(!r.ok)throw Error(await r.text());return loadUsers()}).catch(e=>status.textContent=e.message)};
 document.getElementById('create').onsubmit=e=>{e.preventDefault();const f=new FormData(e.target);const u=selectedUser();if(!u||!u.discord_user_id){status.textContent='Choose a linked Discord user.';return}api('/api/reminders',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({Title:f.get('title'),CreatorID:u.discord_user_id,DeliveryAt:new Date(f.get('delivery')).toISOString(),Timezone:f.get('timezone')})}).then(async r=>{if(!r.ok)throw Error(await r.text());status.textContent='Reminder created.';e.target.elements.title.value='';calendar.refetchEvents()}).catch(e=>status.textContent=e.message)};
 loadUsers().catch(e=>status.textContent=e.message);
});
</script>
</body>
</html>`
