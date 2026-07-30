package database

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/JeayGucci/Discord-Task-Bot/internal/reminders"
	"github.com/JeayGucci/Discord-Task-Bot/internal/users"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct {
	pool            *pgxpool.Pool
	defaultTimezone string
}

func Open(ctx context.Context, databasePublicURL, defaultTimezone string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databasePublicURL)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &Store{pool: pool, defaultTimezone: defaultTimezone}, nil
}

func (s *Store) Close()                         { s.pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) CreateDashboardSession(ctx context.Context, tokenHash []byte, username, csrfToken string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO dashboard_sessions(token_hash,username,csrf_token,expires_at) VALUES($1,$2,$3,$4)`, tokenHash, username, csrfToken, expiresAt.UTC())
	return err
}

func (s *Store) GetDashboardSession(ctx context.Context, tokenHash []byte) (string, string, time.Time, error) {
	var username, csrf string
	var expires time.Time
	err := s.pool.QueryRow(ctx, `SELECT username,csrf_token,expires_at FROM dashboard_sessions WHERE token_hash=$1 AND expires_at>now()`, tokenHash).Scan(&username, &csrf, &expires)
	return username, csrf, expires, err
}

func (s *Store) DeleteDashboardSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM dashboard_sessions WHERE token_hash=$1`, tokenHash)
	return err
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1)`, entry.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		contents, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		up := strings.Split(string(contents), "-- +goose Down")[0]
		up = strings.Replace(up, "-- +goose Up", "", 1)
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, up); err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, entry.Name())
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Create(ctx context.Context, p reminders.CreateParams) (reminders.Reminder, error) {
	if err := p.Validate(time.Now()); err != nil {
		return reminders.Reminder{}, err
	}
	r := reminders.Reminder{ID: uuid.New(), Title: strings.TrimSpace(p.Title), Description: strings.TrimSpace(p.Description), CreatorID: p.CreatorID, GuildID: p.GuildID, ChannelID: p.ChannelID, MentionTarget: p.MentionTarget, DeliveryAt: p.DeliveryAt.UTC(), Timezone: p.Timezone, Status: reminders.StatusScheduled}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO reminders (id,title,description,creator_id,guild_id,channel_id,mention_target,delivery_at,timezone,next_attempt_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$8)
		RETURNING created_at,updated_at`, r.ID, r.Title, r.Description, r.CreatorID, r.GuildID, r.ChannelID, r.MentionTarget, r.DeliveryAt, r.Timezone).Scan(&r.CreatedAt, &r.UpdatedAt)
	return r, err
}

const reminderColumns = `id,title,description,creator_id,guild_id,channel_id,mention_target,delivery_at,timezone,status,attempts,last_error,discord_message_id,created_at,updated_at`

func scanReminder(row pgx.Row) (reminders.Reminder, error) {
	var r reminders.Reminder
	err := row.Scan(&r.ID, &r.Title, &r.Description, &r.CreatorID, &r.GuildID, &r.ChannelID, &r.MentionTarget, &r.DeliveryAt, &r.Timezone, &r.Status, &r.Attempts, &r.LastError, &r.DiscordMessageID, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, reminders.ErrNotFound
	}
	return r, err
}

func scanUser(row pgx.Row) (users.User, error) {
	var u users.User
	err := row.Scan(&u.ID, &u.DisplayName, &u.DiscordUserID, &u.Timezone, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, users.ErrNotFound
	}
	return u, err
}

func (s *Store) ListUsers(ctx context.Context) ([]users.User, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,display_name,COALESCE(discord_user_id,''),timezone,created_at,updated_at FROM dashboard_users ORDER BY lower(display_name),created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]users.User, 0)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

func (s *Store) CreateUser(ctx context.Context, p users.CreateParams) (users.User, error) {
	if p.Timezone == "" {
		p.Timezone = s.defaultTimezone
	}
	if err := users.Validate(p.DisplayName, p.Timezone); err != nil {
		return users.User{}, err
	}
	u := users.User{ID: uuid.New(), DisplayName: strings.TrimSpace(p.DisplayName), DiscordUserID: strings.TrimSpace(p.DiscordUserID), Timezone: p.Timezone}
	return s.saveUser(ctx, u, true)
}

func (s *Store) UpdateUser(ctx context.Context, p users.UpdateParams) (users.User, error) {
	if p.Timezone == "" {
		p.Timezone = s.defaultTimezone
	}
	if err := users.Validate(p.DisplayName, p.Timezone); err != nil {
		return users.User{}, err
	}
	u := users.User{ID: p.ID, DisplayName: strings.TrimSpace(p.DisplayName), DiscordUserID: strings.TrimSpace(p.DiscordUserID), Timezone: p.Timezone}
	return s.saveUser(ctx, u, false)
}

func (s *Store) saveUser(ctx context.Context, u users.User, create bool) (users.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return users.User{}, err
	}
	defer tx.Rollback(ctx)
	discordID := any(nil)
	if u.DiscordUserID != "" {
		discordID = u.DiscordUserID
	}
	var row pgx.Row
	if create {
		row = tx.QueryRow(ctx, `INSERT INTO dashboard_users(id,display_name,discord_user_id,timezone) VALUES($1,$2,$3,$4) RETURNING id,display_name,COALESCE(discord_user_id,''),timezone,created_at,updated_at`, u.ID, u.DisplayName, discordID, u.Timezone)
	} else {
		row = tx.QueryRow(ctx, `UPDATE dashboard_users SET display_name=$2,discord_user_id=$3,timezone=$4,updated_at=now() WHERE id=$1 RETURNING id,display_name,COALESCE(discord_user_id,''),timezone,created_at,updated_at`, u.ID, u.DisplayName, discordID, u.Timezone)
	}
	saved, err := scanUser(row)
	if err != nil {
		return users.User{}, err
	}
	if saved.DiscordUserID != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO users(discord_user_id,display_name,timezone) VALUES($1,$2,$3) ON CONFLICT(discord_user_id) DO UPDATE SET display_name=excluded.display_name,timezone=excluded.timezone,updated_at=now()`, saved.DiscordUserID, saved.DisplayName, saved.Timezone); err != nil {
			return users.User{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return users.User{}, err
	}
	return saved, nil
}

func (s *Store) DeleteUser(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM dashboard_users WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return users.ErrNotFound
	}
	return err
}

func (s *Store) Get(ctx context.Context, id uuid.UUID) (reminders.Reminder, error) {
	return scanReminder(s.pool.QueryRow(ctx, `SELECT `+reminderColumns+` FROM reminders WHERE id=$1`, id))
}

func (s *Store) Update(ctx context.Context, p reminders.UpdateParams) (reminders.Reminder, error) {
	if strings.TrimSpace(p.Title) == "" || len([]rune(p.Title)) > 200 {
		return reminders.Reminder{}, errors.New("title is required and must be at most 200 characters")
	}
	if !p.DeliveryAt.After(time.Now()) {
		return reminders.Reminder{}, errors.New("delivery time must be in the future")
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return reminders.Reminder{}, fmt.Errorf("invalid timezone: %w", err)
	}
	return scanReminder(s.pool.QueryRow(ctx, `UPDATE reminders SET title=$3,delivery_at=$4,next_attempt_at=$4,timezone=$5,status='scheduled',claimed_at=NULL,last_error='',updated_at=now() WHERE id=$1 AND creator_id=$2 AND status IN ('scheduled','processing','failed') RETURNING `+reminderColumns, p.ID, p.CreatorID, strings.TrimSpace(p.Title), p.DeliveryAt.UTC(), p.Timezone))
}

func (s *Store) List(ctx context.Context, f reminders.ListFilter) ([]reminders.Reminder, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT ` + reminderColumns + ` FROM reminders WHERE 1=1`
	args := []any{}
	add := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	if f.CreatorID != "" {
		query += ` AND creator_id=` + add(f.CreatorID)
	}
	if f.GuildID != "" {
		query += ` AND guild_id=` + add(f.GuildID)
	}
	if !f.From.IsZero() {
		query += ` AND delivery_at>=` + add(f.From.UTC())
	}
	if !f.To.IsZero() {
		query += ` AND delivery_at<` + add(f.To.UTC())
	}
	if len(f.Statuses) > 0 {
		statuses := make([]string, len(f.Statuses))
		for i, status := range f.Statuses {
			statuses[i] = string(status)
		}
		query += ` AND status=ANY(` + add(statuses) + `)`
	}
	query += ` ORDER BY delivery_at ASC LIMIT ` + add(limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]reminders.Reminder, 0)
	for rows.Next() {
		r, err := scanReminder(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) SetStatus(ctx context.Context, id uuid.UUID, creatorID string, status reminders.Status) error {
	tag, err := s.pool.Exec(ctx, `UPDATE reminders SET status=$3,updated_at=now() WHERE id=$1 AND creator_id=$2 AND status NOT IN ('sent','cancelled')`, id, creatorID, status)
	if err == nil && tag.RowsAffected() == 0 {
		return reminders.ErrNotFound
	}
	return err
}

func (s *Store) SetTimezone(ctx context.Context, userID, timezone string) error {
	if _, err := time.LoadLocation(timezone); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO users(discord_user_id,timezone) VALUES($1,$2) ON CONFLICT(discord_user_id) DO UPDATE SET timezone=excluded.timezone,updated_at=now()`, userID, timezone)
	return err
}

func (s *Store) GetTimezone(ctx context.Context, userID, guildID string) (string, error) {
	var timezone string
	err := s.pool.QueryRow(ctx, `SELECT COALESCE((SELECT timezone FROM users WHERE discord_user_id=$1),(SELECT timezone FROM guilds WHERE discord_guild_id=$2),$3)`, userID, guildID, s.defaultTimezone).Scan(&timezone)
	return timezone, err
}

func (s *Store) ClaimDue(ctx context.Context, now time.Time, limit int) ([]reminders.Reminder, error) {
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT id FROM reminders
			WHERE status IN ('scheduled','processing') AND next_attempt_at <= $1
			  AND (claimed_at IS NULL OR claimed_at < $1 - interval '5 minutes')
			ORDER BY next_attempt_at FOR UPDATE SKIP LOCKED LIMIT $2
		)
		UPDATE reminders r SET status='processing',claimed_at=$1,attempts=r.attempts+1,updated_at=$1
		FROM due WHERE r.id=due.id RETURNING r.id,r.title,r.description,r.creator_id,r.guild_id,r.channel_id,r.mention_target,r.delivery_at,r.timezone,r.status,r.attempts,r.last_error,r.discord_message_id,r.created_at,r.updated_at`, now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []reminders.Reminder
	for rows.Next() {
		r, err := scanReminder(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) MarkSent(ctx context.Context, id uuid.UUID, messageID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	key := "reminder:" + id.String()
	if _, err = tx.Exec(ctx, `INSERT INTO reminder_deliveries(reminder_id,idempotency_key,discord_message_id,result) VALUES($1,$2,$3,'sent') ON CONFLICT(idempotency_key) DO NOTHING`, id, key, messageID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE reminders SET status='sent',discord_message_id=$2,claimed_at=NULL,last_error='',updated_at=now() WHERE id=$1`, id, messageID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) MarkFailed(ctx context.Context, id uuid.UUID, cause error, retryAt time.Time) error {
	message := cause.Error()
	if len(message) > 1000 {
		message = message[:1000]
	}
	_, err := s.pool.Exec(ctx, `UPDATE reminders SET status=CASE WHEN attempts>=5 THEN 'failed' ELSE 'scheduled' END,next_attempt_at=$2,claimed_at=NULL,last_error=$3,updated_at=now() WHERE id=$1`, id, retryAt.UTC(), message)
	return err
}

func (s *Store) ResetConversation(ctx context.Context, userID, channelID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM conversations WHERE user_id=$1 AND channel_id=$2`, userID, channelID)
	return err
}

func (s *Store) GetConversation(ctx context.Context, userID, channelID string) (string, error) {
	var responseID string
	err := s.pool.QueryRow(ctx, `SELECT previous_response_id FROM conversations WHERE user_id=$1 AND channel_id=$2 AND expires_at>now()`, userID, channelID).Scan(&responseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return responseID, err
}

func (s *Store) SaveConversation(ctx context.Context, userID, guildID, channelID, responseID string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO conversations(id,guild_id,channel_id,user_id,previous_response_id,expires_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(channel_id,user_id) DO UPDATE SET guild_id=excluded.guild_id,previous_response_id=excluded.previous_response_id,expires_at=excluded.expires_at,updated_at=now()`, uuid.New(), guildID, channelID, userID, responseID, expiresAt.UTC())
	return err
}

func (s *Store) DeleteUserData(ctx context.Context, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, q := range []string{`DELETE FROM conversations WHERE user_id=$1`, `DELETE FROM ai_tool_runs WHERE user_id=$1`, `DELETE FROM reminders WHERE creator_id=$1`, `DELETE FROM users WHERE discord_user_id=$1`} {
		if _, err := tx.Exec(ctx, q, userID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
