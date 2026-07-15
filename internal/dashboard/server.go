package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmantheitguy/Discord-Task-Bot/internal/reminders"
	"golang.org/x/crypto/bcrypt"
)

type Pinger interface{ Ping(context.Context) error }
type SessionStore interface {
	CreateDashboardSession(context.Context, []byte, string, string, time.Time) error
	GetDashboardSession(context.Context, []byte) (string, string, time.Time, error)
	DeleteDashboardSession(context.Context, []byte) error
}

type Server struct {
	store           reminders.Store
	db              Pinger
	sessions        SessionStore
	username        string
	passwordHash    []byte
	ownerID         string
	defaultTimezone string
	logger          *slog.Logger
}

func New(store reminders.Store, db Pinger, sessions SessionStore, username, passwordHash, password, ownerID, defaultTimezone string, logger *slog.Logger) *Server {
	hash := []byte(passwordHash)
	if len(hash) == 0 && password != "" {
		hash, _ = bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	}
	return &Server{store: store, db: db, sessions: sessions, username: username, passwordHash: hash, ownerID: ownerID, defaultTimezone: defaultTimezone, logger: logger}
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
	mux.HandleFunc("GET /", s.auth(s.index))
	mux.HandleFunc("GET /api/reminders", s.auth(s.list))
	mux.HandleFunc("POST /api/reminders", s.auth(s.create))
	mux.HandleFunc("POST /api/reminders/{id}/cancel", s.auth(s.cancel))
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
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(strings.ReplaceAll(indexHTML, "{{CSRF}}", csrf)))
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, _ := time.Parse(time.RFC3339, q.Get("start"))
	to, _ := time.Parse(time.RFC3339, q.Get("end"))
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
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	var in struct{ Title, Description, CreatorID, GuildID, ChannelID, MentionTarget, DeliveryAt, Timezone string }
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in) != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if in.CreatorID == "" {
		in.CreatorID = s.ownerID
	}
	if in.Timezone == "" {
		in.Timezone = s.defaultTimezone
	}
	delivery, err := time.Parse(time.RFC3339, in.DeliveryAt)
	if err != nil {
		http.Error(w, "delivery_at must be RFC3339", 400)
		return
	}
	item, err := s.store.Create(r.Context(), reminders.CreateParams{Title: in.Title, Description: in.Description, CreatorID: in.CreatorID, GuildID: in.GuildID, ChannelID: in.ChannelID, MentionTarget: in.MentionTarget, DeliveryAt: delivery, Timezone: in.Timezone})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
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
	if s.store.SetStatus(r.Context(), id, s.ownerID, reminders.StatusCancelled) != nil {
		http.Error(w, "reminder not found", 404)
		return
	}
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

const loginHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>TaskBot Login</title><style>body{font-family:system-ui;background:#f4f6fa;display:grid;place-items:center;min-height:100vh;margin:0}.card{background:white;padding:32px;border-radius:12px;box-shadow:0 3px 18px #17203320;width:min(360px,calc(100% - 48px))}input,button{box-sizing:border-box;width:100%;padding:11px;margin:7px 0;border:1px solid #ccd2df;border-radius:7px;font:inherit}button{background:#5865f2;color:white;border:0}</style></head><body><form class="card" method="post" action="/login"><h1>TaskBot</h1><p>Sign in to your private calendar.</p><input name="username" autocomplete="username" placeholder="Username" required autofocus><input name="password" type="password" autocomplete="current-password" placeholder="Password" required><button>Sign in</button></form></body></html>`
const indexHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>TaskBot Calendar</title><script src="https://cdn.jsdelivr.net/npm/fullcalendar@6.1.18/index.global.min.js"></script><style>body{font-family:system-ui;margin:0;background:#f4f6fa;color:#172033}header{padding:20px 28px;background:#5865f2;color:white;display:flex;justify-content:space-between;align-items:center}main{max-width:1100px;margin:24px auto;padding:0 20px}.card{background:white;padding:20px;border-radius:12px;box-shadow:0 3px 18px #17203315;margin-bottom:20px}form{display:grid;grid-template-columns:2fr 1.5fr 1.5fr 1fr auto;gap:10px}input,button{font:inherit;padding:10px;border:1px solid #ccd2df;border-radius:7px}button{background:#5865f2;color:white;border:0;cursor:pointer}.status{min-height:24px}.status-completed,.status-sent{opacity:.65}.status-failed{background:#c0392b!important}.status-cancelled{text-decoration:line-through;opacity:.5}@media(max-width:800px){form{grid-template-columns:1fr}}</style></head><body><header><h1>TaskBot Calendar</h1><button id="logout">Sign out</button></header><main><div class="card"><h2>Create reminder</h2><form id="create"><input name="title" placeholder="Reminder title" maxlength="200" required><input name="delivery" type="datetime-local" required><input name="channel" placeholder="Discord channel ID" required><input name="timezone" value="America/New_York" required><button>Create</button></form><div id="status" class="status"></div></div><div class="card"><div id="calendar"></div></div></main><script>const csrf='{{CSRF}}';const api=(p,o={})=>{o.headers={...(o.headers||{}),'X-CSRF-Token':csrf};return fetch(p,o)};document.addEventListener('DOMContentLoaded',()=>{const status=document.getElementById('status');const c=new FullCalendar.Calendar(document.getElementById('calendar'),{initialView:'dayGridMonth',headerToolbar:{left:'prev,next today',center:'title',right:'dayGridMonth,timeGridWeek,listMonth'},events:(i,ok,fail)=>api('/api/reminders?start='+encodeURIComponent(i.startStr)+'&end='+encodeURIComponent(i.endStr)).then(r=>r.ok?r.json():Promise.reject(Error('Unable to load reminders'))).then(ok).catch(fail),eventClick:i=>{if(confirm('Cancel '+i.event.title+'?'))api('/api/reminders/'+i.event.id+'/cancel',{method:'POST'}).then(r=>{if(!r.ok)throw Error('Cancel failed');c.refetchEvents()}).catch(e=>status.textContent=e.message)}});c.render();document.getElementById('logout').onclick=()=>api('/logout',{method:'POST'}).then(()=>location='/login');document.getElementById('create').onsubmit=e=>{e.preventDefault();const f=new FormData(e.target);api('/api/reminders',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({Title:f.get('title'),ChannelID:f.get('channel'),DeliveryAt:new Date(f.get('delivery')).toISOString(),Timezone:f.get('timezone')})}).then(async r=>{if(!r.ok)throw Error(await r.text());status.textContent='Reminder created.';e.target.elements.title.value='';c.refetchEvents()}).catch(e=>status.textContent=e.message)}});</script></body></html>`
