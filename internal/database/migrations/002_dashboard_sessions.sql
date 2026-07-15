CREATE TABLE IF NOT EXISTS dashboard_sessions (token_hash BYTEA PRIMARY KEY, username TEXT NOT NULL, csrf_token TEXT NOT NULL, expires_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS dashboard_sessions_expiry_idx ON dashboard_sessions (expires_at);
