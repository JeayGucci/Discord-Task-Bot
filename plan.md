# TaskBot Project Plan

Last updated: 2026-07-15

## 1. Project summary

TaskBot is a Discord to-do, reminder, and conversational assistant hosted on Railway. Users can create scheduled reminders through slash commands or by mentioning the bot in natural language. Scheduled reminders appear in a web dashboard with a calendar view. The bot can also answer general questions through the OpenAI API.

The project will be developed in this GitHub repository and deployed from GitHub to Railway.

## 2. Product goals

- Create personal or shared tasks and reminders in Discord.
- Ping a user, role, or channel at a custom time.
- Support reminders relative to an event, such as reminding someone to complete a SOAP note 24 hours beforehand.
- Allow users to list, edit, complete, and cancel reminders.
- Accept natural-language instructions when a user mentions the bot.
- Provide general AI conversation when a user mentions the bot.
- Display scheduled reminders in a secure web calendar.
- Preserve all reminders across deployments and Railway restarts.
- Keep AI usage and hosting costs low for a personal deployment.
- Leave room for Google Calendar or Outlook integration later.

## 3. MVP scope

### Discord commands

The initial slash-command interface should include:

- `/remind create`
- `/remind list`
- `/remind edit`
- `/remind cancel`
- `/remind complete`
- `/todo create`
- `/timezone set`
- `/chat reset`
- `/privacy delete-my-data`

Slash commands are the reliable fallback and do not require an OpenAI API call.

### Natural-language interactions

When mentioned, the bot should understand requests such as:

```text
@TaskBot remind me to finish my SOAP note tomorrow at 4 PM
@TaskBot remind @NurseRole 24 hours before our meeting Friday at 2 PM
@TaskBot what reminders do I have next week?
@TaskBot move my SOAP note reminder to 6 PM
```

The bot should also support ordinary questions and conversation:

```text
@TaskBot help me organize today's tasks
@TaskBot explain the difference between SOAP and DAP notes
```

### Dashboard

The MVP dashboard should provide:

- Discord OAuth login.
- Server-side guild and permission checks.
- Month, week, day, and list calendar views.
- Reminder creation and editing.
- Filtering by server, channel, creator, and status.
- Status colors for scheduled, sent, completed, cancelled, and failed reminders.
- Links to associated Discord messages when available.

### Deferred features

These are planned after the MVP is stable:

- Google Calendar synchronization.
- Outlook Calendar synchronization.
- Advanced recurring reminders.
- Rich natural-language recurrence parsing.
- Reminder templates.
- Multi-server administration features.

## 4. Technical architecture

Go is the preferred implementation language. The first deployment will use one Go application containing the Discord bot, HTTP dashboard, AI integration, and reminder scheduler. PostgreSQL will provide durable state.

```text
Discord slash commands -----+
Discord bot mentions -------+----> Go application ----> PostgreSQL
Web dashboard --------------+          |
                                        +----> OpenAI Responses API
                                        |
                                        +----> Discord reminder delivery
```

The application can be split into separate web and worker services later if load or reliability requirements justify it.

### Recommended stack

- Go, using the stable version supported by Railway at implementation time.
- `discordgo` for Discord interactions.
- PostgreSQL hosted on Railway.
- `pgx` for PostgreSQL access.
- `goose` for database migrations.
- Go `net/http` with a lightweight router.
- Server-rendered HTML and FullCalendar for the dashboard.
- Discord OAuth2 for dashboard authentication.
- OpenAI Responses API for AI conversation and structured tool calls.
- Docker for repeatable local and Railway builds.
- GitHub Actions for formatting, tests, static analysis, and builds.

## 5. OpenAI integration

### Model strategy

The default model should be configurable:

```text
OPENAI_CHAT_MODEL=gpt-5-nano
```

`gpt-5-nano` is the initial choice because this is a low-volume personal bot and cost is the primary concern. Model names and pricing must be rechecked against official OpenAI documentation before implementation or deployment because they can change.

If evaluations show that `gpt-5-nano` is not reliable enough for date interpretation or structured tool calls, try `gpt-5-mini` for those requests. Do not default to `gpt-5.5`; its cost is not justified for routine reminder scheduling or casual bot conversation.

ChatGPT Plus does not provide API usage for this bot. OpenAI API usage is billed separately. The bot must have its own API key, project budget, and usage alerts.

### Cost-control routing

```text
Slash commands         -> Go only; no model call
Clear date expressions -> Prefer deterministic Go parsing
Ambiguous language     -> OpenAI model
General conversation   -> OpenAI model, only when invoked
Reminder delivery      -> Go and PostgreSQL; no model call
```

Additional controls:

- Restrict initial access to the owner's Discord account or selected server.
- Keep prompts concise.
- Limit response length.
- Retain only the conversation context needed for the current interaction.
- Configure an OpenAI project budget and usage alerts.
- Record token usage and estimated cost per request.
- Fail gracefully: reminders, slash commands, and the dashboard must continue working when AI is unavailable or the budget is exhausted.

### Application-owned tools

The model may request narrowly defined operations such as:

- `create_reminder`
- `update_reminder`
- `cancel_reminder`
- `complete_reminder`
- `list_reminders`
- `create_task`
- `get_user_timezone`

The model must never write directly to PostgreSQL or send scheduled pings. It proposes structured tool arguments; Go validates permissions, dates, timezones, mentions, and database state before executing anything.

### AI behavior and confirmation rules

- The current time and user timezone come from the application, not model memory.
- Relative dates are interpreted in the user's configured timezone.
- Missing material details trigger a clarification question.
- The bot never claims a reminder was created until the database write succeeds.
- Tool and database results are authoritative.
- The model must not invent reminder IDs, roles, users, or channels.
- General conversation must not silently perform an action.
- Potentially clinical content must not be logged unnecessarily, and the bot must not represent itself as providing professional medical advice.

Require explicit confirmation for:

- `@everyone`, `@here`, or large-role pings.
- Editing or cancelling another person's reminder.
- Recurring reminders without an end date.
- Low-confidence date or timezone interpretations.
- Bulk operations.
- External calendar writes.

## 6. Scheduling and reliability

Reminders must be stored in PostgreSQL rather than held only in application memory. A worker loop should periodically claim due reminders and deliver them through Discord.

Required guarantees:

- Transactional row claiming prevents multiple workers from processing the same reminder.
- An idempotency key prevents duplicate Discord messages after crashes or retries.
- Transient Discord errors use bounded exponential backoff.
- Permanent failures remain visible in the dashboard.
- All instants are stored in UTC.
- The user's IANA timezone, such as `America/New_York`, is retained for display and editing.
- Daylight-saving transitions are covered by tests.
- Railway restarts and deployments do not lose scheduled work.

## 7. Data model

### `users`

- Discord user ID.
- Display name.
- Preferred timezone.
- Privacy and retention preferences.

### `guilds`

- Discord guild ID.
- Default timezone.
- Allowed channels and roles.
- Permission and configuration settings.

### `reminders`

- Reminder ID.
- Title and optional description.
- Creator.
- Guild and channel.
- User or role mention target.
- Optional task/event time.
- Reminder offset, such as 24 hours.
- Delivery time stored in UTC.
- Original timezone.
- Status: scheduled, processing, sent, completed, cancelled, or failed.
- Retry and error information.
- Created and updated timestamps.

### `reminder_deliveries`

- Reminder ID.
- Idempotency key.
- Attempt timestamp.
- Discord message ID.
- Delivery result or error.

### `conversations`

- Discord guild, channel/thread, and user identifiers.
- Latest OpenAI response identifier when used.
- Conversation mode.
- Expiration timestamp.

### `conversation_messages`

- Conversation ID.
- User or assistant role.
- Message content.
- Discord message ID.
- Created and expiration timestamps.

### `ai_tool_runs`

- Requested tool and structured arguments.
- Validation result.
- Confirmation state.
- Execution result.
- Token usage, latency, and API trace identifier.

### `oauth_sessions`

- Hashed session identifier.
- Discord identity.
- Expiration timestamp.

## 8. Security and privacy

- Keep Discord and OpenAI secrets only in Railway environment variables.
- Never expose the bot token or OpenAI API key to browser code.
- Authenticate the dashboard through Discord OAuth2.
- Recheck guild membership and permissions on the server for every protected action.
- Restrict broad role and channel pings to authorized users.
- Isolate conversation history by user, channel, and guild.
- Provide `/chat reset` and `/privacy delete-my-data`.
- Start with short conversation retention, tentatively seven days.
- Avoid storing sensitive clinical details in logs.
- Treat SOAP-note reminders as potentially sensitive. Discord and this application must not be assumed to be HIPAA compliant without a separate legal, security, vendor, and operational review.
- Use stable, privacy-preserving safety identifiers in OpenAI requests when supported and recommended by the current API documentation.

## 9. Proposed repository layout

```text
TaskBot/
|-- cmd/taskbot/main.go
|-- internal/
|   |-- auth/
|   |-- bot/
|   |-- config/
|   |-- database/
|   |-- dashboard/
|   |-- openai/
|   |-- reminders/
|   `-- scheduler/
|-- migrations/
|-- web/
|   |-- static/
|   `-- templates/
|-- tests/
|-- Dockerfile
|-- compose.yaml
|-- railway.json
|-- go.mod
|-- .env.example
|-- .github/workflows/ci.yml
`-- plan.md
```

## 10. Implementation phases

### Phase 1: Foundation

- Initialize the Go module and application packages.
- Add configuration loading and validation.
- Add structured logging and request identifiers.
- Add PostgreSQL, migrations, and local Docker Compose.
- Add health and readiness endpoints.
- Add Docker and Railway configuration.
- Add GitHub Actions for formatting, tests, vetting, and builds.

### Phase 2: Discord reminder MVP

- Register the Discord application and bot.
- Implement slash commands.
- Add timezone preferences.
- Store, list, edit, complete, and cancel reminders.
- Enforce guild, channel, role, and ownership permissions.

### Phase 3: Reliable delivery

- Implement database-backed reminder claiming.
- Send scheduled Discord pings.
- Add retry policies and idempotency.
- Record delivery attempts and expose failures.
- Test restart and concurrent-worker behavior.

### Phase 4: Natural language and chat

- Integrate the OpenAI Responses API.
- Route Discord mentions to general chat or structured tools.
- Implement application-owned reminder tools.
- Add clarification and confirmation flows.
- Add conversation reset and retention behavior.
- Track token usage, latency, errors, and estimated cost.
- Build an evaluation set for date parsing and safe action behavior.

### Phase 5: Dashboard

- Add Discord OAuth login and sessions.
- Implement server-side authorization.
- Add reminder list and FullCalendar views.
- Add create, edit, complete, and cancel forms.
- Add filters and delivery failure details.

### Phase 6: Production hardening and deployment

- Create the GitHub repository and push the reviewed code.
- Connect GitHub to Railway.
- Provision Railway PostgreSQL.
- Configure secrets and deployment health checks.
- Define a safe production migration process.
- Add metrics, error reporting, and alerts.
- Add backup and restore documentation.
- Set OpenAI project budgets and usage alerts.

### Phase 7: Calendar integrations

- Begin with Google Calendar after the MVP is stable.
- Add provider OAuth and encrypted token storage.
- Decide whether synchronization is one-way or bidirectional.
- Define conflict, deletion, and recurrence behavior.
- Add Outlook only if it is still needed.

## 11. Testing strategy

- Unit tests for date parsing, timezone conversion, authorization, and reminder state transitions.
- Integration tests using PostgreSQL for claiming, retries, and idempotency.
- HTTP tests for dashboard authentication and authorization.
- Mocked Discord and OpenAI API tests.
- AI evaluations for relative dates, missing details, ambiguous dates, role mentions, prompt injection, and attempted unauthorized actions.
- Tests for daylight-saving boundaries and Railway-style restarts.
- End-to-end smoke test in a private Discord test server before production deployment.

## 12. Initial operating assumptions

- This begins as a personal, low-volume bot.
- Access is initially limited to the owner or an allowlisted Discord server.
- One Go service runs the bot, dashboard, scheduler, and AI integration.
- PostgreSQL is required in deployed environments.
- Users can have personal reminders; the schema also supports shared guild reminders.
- Slash commands remain usable without OpenAI.
- External calendar synchronization is not required for the MVP.
- Conversation history is short-lived unless a later product decision changes it.

## 13. Decisions still required

- Whether reminders are personal only, shared within a guild, or both in the first release.
- Which Discord roles may create role-wide reminders.
- Whether straightforward natural-language reminders execute immediately or always require confirmation.
- Exact conversation retention period.
- Whether general AI chat is available to every allowed server member or only selected users.
- Railway service and PostgreSQL plan after current pricing is reviewed.
- Whether Google Calendar synchronization will be one-way or bidirectional.

## 14. Definition of MVP completion

The MVP is complete when an authorized user can create a reminder through a slash command or natural-language Discord mention, the reminder survives a deployment, the bot delivers exactly one ping at the correct timezone-aware time, and the user can view and manage that reminder in an authenticated web calendar. General AI conversation works within a configured cost limit, and the bot's non-AI features continue to operate when the OpenAI API is unavailable.

## 15. Planning authority

This file is the current source of truth for TaskBot scope and architecture. Future implementation work should consult it before making material design decisions. When requirements or architectural decisions change, update this file and its `Last updated` date as part of the same change.
