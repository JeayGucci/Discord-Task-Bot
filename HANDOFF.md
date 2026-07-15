# TaskBot Continuation Handoff

Last updated: 2026-07-15

This document captures the decisions and implementation context from the original planning and build conversation so work can continue from another computer. It intentionally excludes Discord bot tokens, OpenAI API keys, database credentials, passwords, session cookies, and other secrets.

## Current state

- GitHub repository: <https://github.com/jmantheitguy/Discord-Task-Bot>
- Production dashboard: <https://discord-task-bot-production.up.railway.app/>
- Health endpoint: <https://discord-task-bot-production.up.railway.app/healthz>
- Readiness endpoint: <https://discord-task-bot-production.up.railway.app/readyz>
- Hosting: Railway
- Database: Railway PostgreSQL
- Language: Go
- Default AI model: `gpt-5-nano`
- Main branch currently deploys automatically through Railway.
- GitHub Actions validates formatting, vetting, race-enabled tests, compilation, and the Docker image.

At the end of the original implementation session, GitHub CI passed, `/healthz` returned `200`, `/readyz` returned `200`, and the dashboard redirected unauthenticated visitors to `/login`.

## Product decisions from the conversation

1. TaskBot is a Discord to-do and reminder bot.
2. Users can create reminders at custom times and receive Discord pings.
3. A primary example is reminding a user to complete a SOAP note 24 hours before it is due.
4. Users can create, list, edit, complete, and cancel reminders.
5. Users can mention the bot in natural language to schedule a reminder.
6. Users can also mention the bot for ordinary ChatGPT-style conversation.
7. OpenAI API billing is separate from ChatGPT Plus.
8. Cost is prioritized, so the default model is `gpt-5-nano`. Slash commands and delivery do not call OpenAI.
9. The calendar is native to TaskBot. Do not add Google Calendar, Outlook, or other external calendar synchronization.
10. Do not add Discord OAuth to the dashboard.
11. The dashboard uses its own local username/password login.
12. Plaintext `DASHBOARD_PASSWORD` configuration is supported at the owner's request. A bcrypt hash remains supported as the safer alternative.
13. Dashboard sessions are server-side, expire after 24 hours, use `HttpOnly`/`SameSite=Strict` cookies, and require CSRF tokens for mutations.
14. The application uses `DATABASE_PUBLIC_URL` everywhere, not `DATABASE_URL`.
15. Operational Discord audit logs go to channel `1526851837221671043`.
16. The bot uses a streaming presence labeled `Streamlining your tasks`, linked to the configured dashboard URL.
17. Functional behavior was prioritized over visual design. UI fine-tuning comes later.

## Discord identifiers

These identifiers are public operational configuration, not credentials:

```text
Application ID: 1526848626150871112
Guild ID:       1526434983823278202
Owner user ID:  953631701056229426
Log channel ID: 1526851837221671043
```

The Discord bot token is sealed in Railway and is not stored here.

## Railway variables

The Railway application service requires the following configuration. Replace secret placeholders using the existing sealed values; never commit their values.

```env
APP_ENV=production
DATABASE_PUBLIC_URL=${{Postgres.DATABASE_PUBLIC_URL}}
DISCORD_BOT_TOKEN=SEALED_IN_RAILWAY
DISCORD_APPLICATION_ID=1526848626150871112
DISCORD_GUILD_ID=1526434983823278202
DISCORD_OWNER_ID=953631701056229426
DISCORD_LOG_CHANNEL_ID=1526851837221671043
OPENAI_API_KEY=SEALED_IN_RAILWAY
OPENAI_CHAT_MODEL=gpt-5-nano
OPENAI_BASE_URL=https://api.openai.com/v1
DASHBOARD_USERNAME=admin
DASHBOARD_PASSWORD=SEALED_IN_RAILWAY
DASHBOARD_PASSWORD_HASH=
DASHBOARD_BASE_URL=https://discord-task-bot-production.up.railway.app
DEFAULT_TIMEZONE=America/New_York
SCHEDULER_INTERVAL=15s
SCHEDULER_CLAIM_LIMIT=25
```

Railway supplies `PORT`; do not set it manually. The application defaults to `8080` outside Railway.

## Implemented functionality

### Discord

- `/remind create`
- `/remind list`
- `/remind edit`
- `/remind cancel`
- `/remind complete`
- `/todo create`
- `/timezone set`
- `/chat reset`
- `/privacy delete-my-data`
- Natural-language reminder creation when the bot is mentioned
- General AI conversation when the bot is mentioned
- Owner allowlisting through `DISCORD_OWNER_ID`
- Guild-scoped command registration during development/initial deployment
- Streaming activity linked to the web dashboard
- Privacy-conscious operational audit messages

### Reminder delivery

- PostgreSQL persistence
- UTC storage with IANA timezone retention
- Transactional due-row claiming using `FOR UPDATE SKIP LOCKED`
- Retry backoff
- Delivery attempt records and idempotency keys
- Failed-delivery state visible through stored reminder status
- Scheduled reminders survive restarts and deployments

### OpenAI

- Responses API integration
- Application-owned `create_reminder` function tool
- Go validates and commits all reminder changes; the model cannot access PostgreSQL directly
- Seven-day conversation continuity using previous response IDs
- Stable privacy-preserving hashed safety identifiers
- AI failures do not stop slash commands, the dashboard, or reminder delivery

### Dashboard

- Local login page
- Username/password verification
- Optional bcrypt password hash support
- PostgreSQL-backed 24-hour sessions
- Secure session cookies
- CSRF protection
- Logout
- FullCalendar month, week, and list views
- Reminder creation
- Click-to-cancel reminders
- Status styling
- `/healthz` and `/readyz`

## Repository guide

- `plan.md`: authoritative product and architecture plan
- `README.md`: setup, commands, and deployment documentation
- `cmd/taskbot/main.go`: application composition and lifecycle
- `cmd/hash-password/main.go`: optional bcrypt hash generator
- `internal/bot/bot.go`: Discord commands, mentions, delivery, audit logging, and presence
- `internal/openai/client.go`: Responses API client and tool schema
- `internal/reminders/reminder.go`: reminder domain and store contract
- `internal/database/postgres.go`: PostgreSQL implementation and migrations
- `internal/scheduler/scheduler.go`: due reminder worker and retries
- `internal/dashboard/server.go`: login, sessions, CSRF, API, and calendar UI
- `migrations/`: human-readable production migrations
- `internal/database/migrations/`: copies embedded into the Go binary; keep these synchronized with `migrations/`
- `railway.json`: Railway build, health check, and restart settings
- `Dockerfile`: production container
- `.github/workflows/ci.yml`: GitHub Actions validation

## Local continuation

Clone and inspect:

```bash
git clone https://github.com/jmantheitguy/Discord-Task-Bot.git
cd Discord-Task-Bot
cp .env.example .env
```

Start PostgreSQL:

```bash
docker compose up -d postgres
```

Load local variables and run:

```bash
set -a
source .env
set +a
go run ./cmd/taskbot
```

Validate changes:

```bash
gofmt -w $(find . -name '*.go' -not -path './vendor/*')
go test -race ./...
go vet ./...
go build ./cmd/taskbot
git diff --check
```

## Known limitations and likely next work

- The UI is intentionally utilitarian and needs visual/design refinement.
- The dashboard is a single-owner local-account model rather than a full multi-user account system.
- Dashboard reminder creation currently requires a Discord channel ID.
- Natural-language tools currently focus on reminder creation; conversational editing, listing, completion, and cancellation can be expanded.
- Advanced recurrence is deferred.
- External calendar providers and Discord OAuth are explicitly out of scope.
- A Discord “streaming” presence may be rendered differently by Discord clients when the URL is not a recognized streaming provider, though the gateway status is configured with the requested dashboard URL.
- Exactly-once external Discord delivery cannot be mathematically guaranteed across a crash after Discord accepts a message but before PostgreSQL records it; delivery records reduce ordinary duplication risk.
- The plaintext dashboard password option was explicitly requested. Prefer a long unique value even when it is sealed in Railway.

## Conversation chronology

The original conversation progressed through these milestones:

1. Defined a Discord to-do/reminder bot hosted on Railway and developed in GitHub.
2. Selected Go and PostgreSQL for a lightweight durable service.
3. Added a web calendar dashboard.
4. Added natural-language scheduling and general OpenAI conversation.
5. Selected `gpt-5-nano` as the lowest-cost practical default and preserved non-AI fallbacks.
6. Wrote `plan.md` as the project source of truth.
7. Created the public GitHub repository `jmantheitguy/Discord-Task-Bot`.
8. Implemented the functional MVP, tests, Docker, CI, and Railway configuration.
9. Clarified that the calendar is TaskBot-native, not an external provider integration.
10. Replaced dashboard bearer tokens with a first-party local login system.
11. Added support for plaintext password configuration at the owner's request.
12. Renamed database configuration to `DATABASE_PUBLIC_URL` everywhere.
13. Configured the production Railway variables and public domain.
14. Added Discord audit-channel logging and the streaming dashboard presence.
15. Committed, pushed, passed GitHub CI, and verified the live Railway health/readiness endpoints.

For future work, read `plan.md`, this handoff, and `README.md` before changing architecture or scope.
