# Discord Task Bot

TaskBot is a Go-based Discord reminder and to-do bot with natural-language scheduling through the OpenAI Responses API and its own private web calendar. The calendar is local to TaskBot—there is no Discord OAuth or external calendar synchronization. The application is designed to run as one lightweight Railway service backed by PostgreSQL.

The authoritative product and architecture plan is in [plan.md](plan.md).

## Current functionality

- Durable, timezone-aware reminders stored in PostgreSQL.
- Discord `/remind create`, `/remind list`, `/remind cancel`, and `/remind complete` commands.
- Owner-only `/reminders` command to list all current reminders.
- Discord `/remind edit` for rescheduling and renaming pending reminders.
- `/todo create`, `/chat reset`, and `/privacy delete-my-data`.
- Natural-language reminder creation and general chat when the bot is mentioned.
- Low-cost `gpt-5-nano` default with AI-independent slash commands and delivery.
- Transactional due-reminder claiming, retry backoff, and duplicate-delivery records.
- Local-login calendar dashboard and session-protected reminder API.
- Admin-managed dashboard users with optional Discord ID linking.
- Public dashboard calendar/reminder creation using managed-user and live Discord-channel dropdowns.
- Dashboard defaults reminder creation/filtering to Jeay and channel selection to `#general-to-do-list`, with all users and other channels still selectable.
- Dashboard shows reminders with target user and target channel; past reminders remain available by navigating/scrolling back.
- Health and readiness checks for Railway.
- Admin-only dashboard health, reminder activity, and runtime log panels.
- Structured Railway/stdout logs for AI responses, AI tool actions, slash commands, and reminder delivery.
- Reminder pings are delivered to the selected Discord channel; AI-created reminders default to the channel where the bot was mentioned unless the user mentions another channel.
- Discord-visible operational audit messages are disabled; created-reminder confirmations are posted in the selected reminder channel.
- Discord streaming presence labeled “Streamlining your tasks”.
- `/dashboard` command that returns the private dashboard URL.
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
4. Grant View Channels, Send Messages, Read Message History, and Use Slash Commands permissions for every category/channel the bot should list or post into.
5. Set `DISCORD_BOT_TOKEN` and `DISCORD_APPLICATION_ID`.
6. During development, set `DISCORD_GUILD_ID` to a private test server for immediate command updates.
7. Set `DISCORD_OWNER_ID` to your user ID for owner-only admin commands such as `/reminders`.
8. Set `DISCORD_REMINDER_CHANNEL_ID` as the fallback channel for older reminders that do not have a stored channel.
9. `DISCORD_REGISTER_COMMANDS` defaults to enabled outside production and disabled in production. Temporarily set it to `true` in production only when slash command definitions need to be pushed, then turn it back off.
10. Set `DISCORD_STREAM_URL` to a Twitch or YouTube URL if you want the bot to show a streaming presence. Discord does not render arbitrary URLs as streaming.
11. Put `DASHBOARD_BASE_URL` in the bot profile description in the Developer Portal if you want it visible on the bot profile.

## Railway deployment

1. Create a Railway project from this GitHub repository.
2. Add a Railway PostgreSQL service.
3. Expose its `DATABASE_PUBLIC_URL` to the TaskBot service.
4. Add the values from `.env.example` as Railway variables.
5. Set `DASHBOARD_USERNAME` and either `DASHBOARD_PASSWORD` or the safer `DASHBOARD_PASSWORD_HASH` generated with `go run ./cmd/hash-password`. Never use a common password on the public Railway URL.
6. Deploy. Railway uses `Dockerfile` and checks `/readyz`.
7. Generate a public domain for the dashboard and store it as `DASHBOARD_BASE_URL`.
8. Leave `DISCORD_REGISTER_COMMANDS` unset or `false` for normal production deploys to avoid unnecessary Discord REST calls during restarts. Set it to `true` for one deployment after changing slash commands.

Do not commit `.env`, Discord tokens, OpenAI keys, or production database URLs.

The dashboard login remains a single admin account. From the dashboard, the admin can create managed users, optionally link each one to a Discord user ID, and view health/log/activity panels.
Viewing the calendar and creating reminders from the dashboard do not require login; user management and operational logs require the admin account.

## Commands

```text
/remind create title:Finish SOAP note when:2026-07-18 16:00 user:@User channel:#reminders
/remind list
/remind edit id:abcd1234 title:Finish SOAP note when:2026-07-18 18:00
/remind cancel id:abcd1234
/remind complete id:abcd1234
/reminders
/todo create title:Prepare notes when:2026-07-18 16:00 user:@User channel:#reminders
/chat reset
/dashboard
/privacy delete-my-data
```

Times without an explicit offset use `America/New_York`. Discord displays created reminder timestamps in each viewer's local timezone. Reminder list commands include the target user, target channel, status, and scheduled time.

## Security note

The private dashboard uses a local username/password login, expiring server-side sessions, secure cookies, and CSRF protection. Plaintext password configuration is supported, though a bcrypt hash is safer. Discord OAuth and Google/Outlook calendar synchronization are intentionally out of scope. The application must not be assumed to be HIPAA compliant; do not place patient-identifying information in Discord reminders or application logs.
