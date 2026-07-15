# Discord Task Bot

TaskBot is a Go-based Discord reminder and to-do bot with natural-language scheduling through the OpenAI Responses API and its own private web calendar. The calendar is local to TaskBot—there is no Discord OAuth or external calendar synchronization. The application is designed to run as one lightweight Railway service backed by PostgreSQL.

The authoritative product and architecture plan is in [plan.md](plan.md).

## Current functionality

- Durable, timezone-aware reminders stored in PostgreSQL.
- Discord `/remind create`, `/remind list`, `/remind cancel`, and `/remind complete` commands.
- Discord `/remind edit` for rescheduling and renaming pending reminders.
- `/todo create`, `/timezone set`, `/chat reset`, and `/privacy delete-my-data`.
- Natural-language reminder creation and general chat when the bot is mentioned.
- Low-cost `gpt-5-nano` default with AI-independent slash commands and delivery.
- Transactional due-reminder claiming, retry backoff, and duplicate-delivery records.
- Local-login calendar dashboard and session-protected reminder API.
- Health and readiness checks for Railway.
- Privacy-conscious operational audit messages in a configured Discord channel.
- Discord streaming presence labeled “Streamlining your tasks” and linked to the dashboard.
- Docker, local PostgreSQL, and GitHub Actions CI.

## Local setup

Requirements: Go 1.26+, Docker, a Discord application, and optionally an OpenAI API key.

```bash
cp .env.example .env
docker compose up -d postgres
set -a; source .env; set +a
go run ./cmd/taskbot
```

Open `http://localhost:8080/` and sign in using `DASHBOARD_USERNAME` and `DASHBOARD_PASSWORD`.

The service applies versioned SQL migrations at startup. If Discord credentials are omitted, the dashboard starts but the Discord bot and scheduler remain disabled. If the OpenAI key is omitted, slash commands and existing reminders continue to work while AI mentions report that chat is unavailable.

## Discord setup

1. Create an application and bot in the Discord Developer Portal.
2. Enable the Message Content intent so mentions can be interpreted.
3. Invite the bot with `bot` and `applications.commands` scopes.
4. Grant Send Messages, Read Message History, and Use Slash Commands permissions.
5. Set `DISCORD_BOT_TOKEN` and `DISCORD_APPLICATION_ID`.
6. During development, set `DISCORD_GUILD_ID` to a private test server for immediate command updates.
7. Set `DISCORD_OWNER_ID` to your user ID to keep the initial bot private.

## Railway deployment

1. Create a Railway project from this GitHub repository.
2. Add a Railway PostgreSQL service.
3. Expose its `DATABASE_PUBLIC_URL` to the TaskBot service.
4. Add the values from `.env.example` as Railway variables.
5. Set `DASHBOARD_USERNAME` and either `DASHBOARD_PASSWORD` or the safer `DASHBOARD_PASSWORD_HASH` generated with `go run ./cmd/hash-password`. Never use a common password on the public Railway URL.
6. Deploy. Railway uses `Dockerfile` and checks `/readyz`.
7. Generate a public domain for the dashboard and store it as `DASHBOARD_BASE_URL`.

Do not commit `.env`, Discord tokens, OpenAI keys, or production database URLs.

## Commands

```text
/remind create title:Finish SOAP note when:2026-07-18 16:00
/remind list
/remind edit id:abcd1234 title:Finish SOAP note when:2026-07-18 18:00
/remind cancel id:abcd1234
/remind complete id:abcd1234
/todo create title:Prepare notes when:2026-07-18 16:00
/timezone set name:America/New_York
/chat reset
/privacy delete-my-data
```

Times without an explicit offset use the user's saved IANA timezone. Discord displays created reminder timestamps in each viewer's local timezone.

## Security note

The private dashboard uses a local username/password login, expiring server-side sessions, secure cookies, and CSRF protection. Plaintext password configuration is supported, though a bcrypt hash is safer. Discord OAuth and Google/Outlook calendar synchronization are intentionally out of scope. The application must not be assumed to be HIPAA compliant; do not place patient-identifying information in Discord reminders or application logs.
