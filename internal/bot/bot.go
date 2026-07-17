package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/google/uuid"
	ai "github.com/jmantheitguy/Discord-Task-Bot/internal/openai"
	"github.com/jmantheitguy/Discord-Task-Bot/internal/reminders"
)

type Bot struct {
	session           *discordgo.Session
	store             reminders.Store
	ai                *ai.Client
	appID             string
	guildID           string
	ownerID           string
	logChannelID      string
	reminderChannelID string
	streamURL         string
	dashboardURL      string
	logger            *slog.Logger
	commands          []*discordgo.ApplicationCommand
	auditMu           sync.Mutex
	auditWindow       time.Time
	auditSent         int
	auditDropped      int
	reminderMu        sync.Mutex
	nextReminder      time.Time
}

func New(token, appID, guildID, ownerID, logChannelID, reminderChannelID, streamURL, dashboardURL string, store reminders.Store, aiClient *ai.Client, logger *slog.Logger) (*Bot, error) {
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	s.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentMessageContent
	b := &Bot{session: s, store: store, ai: aiClient, appID: appID, guildID: guildID, ownerID: ownerID, logChannelID: logChannelID, reminderChannelID: reminderChannelID, streamURL: streamURL, dashboardURL: dashboardURL, logger: logger, commands: commandDefinitions()}
	s.AddHandler(b.onInteraction)
	s.AddHandler(b.onMessage)
	return b, nil
}

func (b *Bot) Open(ctx context.Context, registerCommands bool) error {
	if err := b.session.Open(); err != nil {
		return err
	}
	if registerCommands {
		if _, err := b.session.ApplicationCommandBulkOverwrite(b.appID, b.guildID, b.commands); err != nil {
			b.session.Close()
			return fmt.Errorf("register commands: %w", err)
		}
	}
	b.logger.Info("discord bot connected", "guild_scoped", b.guildID != "", "register_commands", registerCommands)
	if b.streamURL != "" {
		if !isDiscordStreamingURL(b.streamURL) {
			b.logger.Warn("Discord streaming presence URL should be a Twitch or YouTube URL", "url", b.streamURL)
		}
		if err := b.session.UpdateStreamingStatus(0, "Streamlining your tasks", b.streamURL); err != nil {
			b.logger.Warn("set Discord streaming presence", "error", err)
			b.audit("⚠️ TaskBot connected, but its streaming presence could not be set.")
		}
	} else {
		b.logger.Warn("Discord streaming presence is disabled; set DISCORD_STREAM_URL to a Twitch or YouTube URL")
	}
	if registerCommands {
		b.audit("✅ TaskBot connected and commands registered.")
	} else {
		b.audit("✅ TaskBot connected.")
	}
	go func() { <-ctx.Done(); _ = b.session.Close() }()
	return nil
}

func (b *Bot) Close() error { return b.session.Close() }

func (b *Bot) SendReminder(ctx context.Context, r reminders.Reminder) (string, error) {
	if err := b.waitForReminderSlot(ctx); err != nil {
		return "", err
	}
	content := strings.TrimSpace(r.MentionTarget + " Reminder: **" + r.Title + "**")
	channelID := b.reminderChannel(r.ChannelID)
	message, err := b.session.ChannelMessageSend(channelID, content)
	if err != nil {
		b.audit(fmt.Sprintf("❌ Reminder delivery failed · `%s` · channel <#%s>", r.ID.String()[:8], channelID))
		return "", err
	}
	b.audit(fmt.Sprintf("🔔 Reminder delivered · `%s` · channel <#%s>", r.ID.String()[:8], channelID))
	return message.ID, nil
}

func (b *Bot) reminderChannel(fallback string) string {
	if strings.TrimSpace(b.reminderChannelID) != "" {
		return b.reminderChannelID
	}
	return fallback
}

func (b *Bot) waitForReminderSlot(ctx context.Context) error {
	const minimumSpacing = 1200 * time.Millisecond
	b.reminderMu.Lock()
	now := time.Now()
	wait := time.Duration(0)
	if now.Before(b.nextReminder) {
		wait = b.nextReminder.Sub(now)
		now = b.nextReminder
	}
	b.nextReminder = now.Add(minimumSpacing)
	b.reminderMu.Unlock()
	if wait == 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (b *Bot) allowed(userID string) bool { return b.ownerID == "" || b.ownerID == userID }
func (b *Bot) isOwner(userID string) bool { return b.ownerID != "" && b.ownerID == userID }

func (b *Bot) dashboardMessage() string {
	if strings.TrimSpace(b.dashboardURL) == "" {
		return "The TaskBot dashboard URL is not configured."
	}
	return "TaskBot dashboard: " + b.dashboardURL
}

func isDiscordStreamingURL(value string) bool {
	u := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(u, "https://twitch.tv/") ||
		strings.HasPrefix(u, "https://www.twitch.tv/") ||
		strings.HasPrefix(u, "https://youtube.com/") ||
		strings.HasPrefix(u, "https://www.youtube.com/")
}

func (b *Bot) onInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type != discordgo.InteractionApplicationCommand {
		return
	}
	user := interactionUser(i)
	if user == nil {
		return
	}
	if !b.allowed(user.ID) {
		b.respond(i, "This bot is currently restricted to its owner.", true)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	data := i.ApplicationCommandData()
	var message string
	var err error
	switch data.Name {
	case "remind":
		message, err = b.handleRemind(ctx, i, data.Options)
	case "reminders":
		message, err = b.handleAllReminders(ctx, i, data.Options)
	case "todo":
		message, err = b.createFromOptions(ctx, i, subOptions(data.Options, "create"))
	case "timezone":
		message, err = b.handleTimezone(ctx, user.ID, data.Options)
	case "chat":
		err = b.store.ResetConversation(ctx, user.ID, i.ChannelID)
		message = "Conversation context reset."
	case "dashboard":
		message = b.dashboardMessage()
	case "privacy":
		err = b.store.DeleteUserData(ctx, user.ID)
		message = "Your stored reminders, preferences, and conversation data were deleted."
	default:
		message = "Unknown command."
	}
	if err != nil {
		b.logger.Error("discord command", "command", data.Name, "error", err)
		b.audit(fmt.Sprintf("❌ Command failed · `/%s` · user <@%s> · `%s`", data.Name, user.ID, truncate(err.Error(), 400)))
		message = "I couldn't complete that request: " + err.Error()
	} else {
		b.logger.Info("discord command completed", "command", data.Name, "user_id", user.ID, "guild_id", i.GuildID, "channel_id", i.ChannelID)
		b.audit(fmt.Sprintf("✅ Command completed · `/%s` · user <@%s>", data.Name, user.ID))
	}
	b.respond(i, message, true)
}

func (b *Bot) handleRemind(ctx context.Context, i *discordgo.InteractionCreate, options []*discordgo.ApplicationCommandInteractionDataOption) (string, error) {
	if len(options) == 0 {
		return "Choose a reminder action.", nil
	}
	action := options[0].Name
	switch action {
	case "create":
		return b.createFromOptions(ctx, i, options[0].Options)
	case "list":
		user := interactionUser(i)
		items, err := b.store.List(ctx, reminders.ListFilter{CreatorID: user.ID, From: time.Now().Add(-24 * time.Hour), Limit: 25})
		if err != nil {
			return "", err
		}
		if len(items) == 0 {
			return "You have no reminders.", nil
		}
		lines := []string{"Your reminders:"}
		for _, r := range items {
			lines = append(lines, fmt.Sprintf("• `%s` **%s** — <t:%d:F> (%s)", r.ID.String()[:8], r.Title, r.DeliveryAt.Unix(), r.Status))
		}
		return strings.Join(lines, "\n"), nil
	case "cancel", "complete":
		idText := optionString(options[0].Options, "id")
		id, err := resolveID(ctx, b.store, interactionUser(i).ID, idText)
		if err != nil {
			return "", err
		}
		status := reminders.StatusCancelled
		if action == "complete" {
			status = reminders.StatusCompleted
		}
		if err := b.store.SetStatus(ctx, id, interactionUser(i).ID, status); err != nil {
			return "", err
		}
		return fmt.Sprintf("Reminder `%s` marked %s.", id.String()[:8], status), nil
	case "edit":
		id, err := resolveID(ctx, b.store, interactionUser(i).ID, optionString(options[0].Options, "id"))
		if err != nil {
			return "", err
		}
		timezone, err := b.store.GetTimezone(ctx, interactionUser(i).ID, i.GuildID)
		if err != nil {
			return "", err
		}
		delivery, err := parseUserTime(optionString(options[0].Options, "when"), timezone, time.Now())
		if err != nil {
			return "", err
		}
		updated, err := b.store.Update(ctx, reminders.UpdateParams{ID: id, CreatorID: interactionUser(i).ID, Title: optionString(options[0].Options, "title"), DeliveryAt: delivery, Timezone: timezone})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Updated **%s** for <t:%d:F>.", updated.Title, updated.DeliveryAt.Unix()), nil
	default:
		return "That action is not implemented yet.", nil
	}
}

func (b *Bot) handleAllReminders(ctx context.Context, i *discordgo.InteractionCreate, options []*discordgo.ApplicationCommandInteractionDataOption) (string, error) {
	user := interactionUser(i)
	if user == nil || !b.isOwner(user.ID) {
		return "Only the bot owner can view all reminders.", nil
	}
	statuses := []reminders.Status{reminders.StatusScheduled, reminders.StatusProcessing, reminders.StatusFailed}
	items, err := b.store.List(ctx, reminders.ListFilter{From: time.Now().Add(-24 * time.Hour), Statuses: statuses, Limit: 100})
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "There are no current reminders.", nil
	}
	lines := []string{"Current reminders:"}
	for _, r := range items {
		lines = append(lines, fmt.Sprintf("• `%s` <@%s> **%s** — <t:%d:F> (%s)", r.ID.String()[:8], r.CreatorID, r.Title, r.DeliveryAt.Unix(), r.Status))
	}
	return strings.Join(lines, "\n"), nil
}

func (b *Bot) createFromOptions(ctx context.Context, i *discordgo.InteractionCreate, options []*discordgo.ApplicationCommandInteractionDataOption) (string, error) {
	target := optionUser(b.session, options, "user")
	if target == nil {
		return "", errors.New("choose the Discord user to ping")
	}
	timezone, err := b.store.GetTimezone(ctx, target.ID, i.GuildID)
	if err != nil {
		return "", err
	}
	delivery, err := parseUserTime(optionString(options, "when"), timezone, time.Now())
	if err != nil {
		return "", err
	}
	channelID := b.reminderChannel(i.ChannelID)
	r, err := b.store.Create(ctx, reminders.CreateParams{Title: optionString(options, "title"), CreatorID: target.ID, GuildID: i.GuildID, ChannelID: channelID, MentionTarget: "<@" + target.ID + ">", DeliveryAt: delivery, Timezone: timezone})
	if err != nil {
		return "", err
	}
	b.logger.Info(
		"slash reminder created",
		"reminder_id", r.ID,
		"title", r.Title,
		"user_id", target.ID,
		"guild_id", i.GuildID,
		"channel_id", channelID,
		"delivery_at", r.DeliveryAt,
		"timezone", r.Timezone,
	)
	return fmt.Sprintf("Created **%s** for <@%s> at <t:%d:F>. ID: `%s`", r.Title, target.ID, r.DeliveryAt.Unix(), r.ID.String()[:8]), nil
}

func (b *Bot) handleTimezone(ctx context.Context, userID string, options []*discordgo.ApplicationCommandInteractionDataOption) (string, error) {
	zone := optionString(subOptions(options, "set"), "name")
	if _, err := time.LoadLocation(zone); err != nil {
		return "", errors.New("use an IANA timezone such as America/New_York")
	}
	return "Timezone set to **" + zone + "**.", b.store.SetTimezone(ctx, userID, zone)
}

func (b *Bot) onMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.Bot || s.State.User == nil || !strings.Contains(m.Content, "<@"+s.State.User.ID+">") {
		return
	}
	if !b.allowed(m.Author.ID) {
		return
	}
	clean := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(m.Content, "<@"+s.State.User.ID+">", ""), "<@!"+s.State.User.ID+">", ""))
	if clean == "" {
		return
	}
	go b.handleMention(m, clean)
}

func (b *Bot) handleMention(m *discordgo.MessageCreate, content string) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	stopTyping := b.startTyping(ctx, m.ChannelID)
	defer stopTyping()
	timezone, err := b.store.GetTimezone(ctx, m.Author.ID, m.GuildID)
	if err != nil {
		b.reply(m, "I couldn't load your timezone.")
		return
	}
	previousID, err := b.store.GetConversation(ctx, m.Author.ID, m.ChannelID)
	if err != nil {
		b.logger.Warn("load conversation", "error", err)
	}
	result, err := b.ai.Respond(ctx, content, ai.Context{Now: time.Now(), Timezone: timezone, UserID: m.Author.ID, GuildID: m.GuildID, ChannelID: m.ChannelID, PreviousResponseID: previousID})
	if err != nil {
		b.logger.Error("AI response", "error", err)
		b.audit(fmt.Sprintf("❌ AI request failed · user <@%s> · `%s`", m.Author.ID, truncate(err.Error(), 400)))
		b.reply(m, "AI chat is temporarily unavailable. Slash commands and existing reminders still work.")
		return
	}
	b.logger.Info(
		"AI response received",
		"user_id", m.Author.ID,
		"guild_id", m.GuildID,
		"channel_id", m.ChannelID,
		"response_id", result.ResponseID,
		"has_tool_action", result.Action != nil,
		"text", truncate(result.Text, 1000),
	)
	if result.ResponseID != "" {
		if err := b.store.SaveConversation(ctx, m.Author.ID, m.GuildID, m.ChannelID, result.ResponseID, time.Now().Add(7*24*time.Hour)); err != nil {
			b.logger.Warn("save conversation", "error", err)
		}
	}
	if result.Action == nil {
		b.logger.Info("AI chat reply sent", "user_id", m.Author.ID, "guild_id", m.GuildID, "channel_id", m.ChannelID)
		b.reply(m, truncate(result.Text, 1900))
		return
	}
	b.logger.Info(
		"AI tool action requested",
		"user_id", m.Author.ID,
		"guild_id", m.GuildID,
		"channel_id", m.ChannelID,
		"tool", result.Action.Name,
		"arguments", truncate(string(result.Action.Arguments), 1000),
	)
	if result.Action.Name != "create_reminder" {
		b.logger.Warn("unsupported AI tool action", "tool", result.Action.Name, "user_id", m.Author.ID, "guild_id", m.GuildID, "channel_id", m.ChannelID)
		b.reply(m, "I can't perform that action yet.")
		return
	}
	var args struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		DeliveryAt  string `json:"delivery_at"`
	}
	if err := decodeArguments(result.Action.Arguments, &args); err != nil {
		b.reply(m, "I couldn't understand the reminder details. Please include an exact date and time.")
		return
	}
	delivery, err := time.Parse(time.RFC3339, args.DeliveryAt)
	if err != nil {
		b.reply(m, "I need an exact date and time before creating that reminder.")
		return
	}
	channelID := b.reminderChannel(m.ChannelID)
	r, err := b.store.Create(ctx, reminders.CreateParams{Title: args.Title, Description: args.Description, CreatorID: m.Author.ID, GuildID: m.GuildID, ChannelID: channelID, MentionTarget: "<@" + m.Author.ID + ">", DeliveryAt: delivery, Timezone: timezone})
	if err != nil {
		b.logger.Error("AI reminder creation failed", "error", err, "user_id", m.Author.ID, "guild_id", m.GuildID, "channel_id", m.ChannelID)
		b.reply(m, "I couldn't create that reminder: "+err.Error())
		return
	}
	b.logger.Info(
		"AI reminder created",
		"reminder_id", r.ID,
		"title", r.Title,
		"user_id", m.Author.ID,
		"guild_id", m.GuildID,
		"channel_id", channelID,
		"delivery_at", r.DeliveryAt,
		"timezone", r.Timezone,
	)
	b.audit(fmt.Sprintf("🤖 Natural-language reminder created · `%s` · user <@%s> · <t:%d:F>", r.ID.String()[:8], m.Author.ID, r.DeliveryAt.Unix()))
	b.reply(m, fmt.Sprintf("Created **%s** for <t:%d:F>. ID: `%s`", r.Title, r.DeliveryAt.Unix(), r.ID.String()[:8]))
}

func (b *Bot) reply(m *discordgo.MessageCreate, text string) {
	_, _ = b.session.ChannelMessageSendReply(m.ChannelID, text, m.Reference())
}

func (b *Bot) startTyping(ctx context.Context, channelID string) func() {
	done := make(chan struct{})
	sendTyping := func() {
		if err := b.session.ChannelTyping(channelID); err != nil {
			b.logger.Warn("send Discord typing indicator", "error", err)
		}
	}
	sendTyping()
	go func() {
		ticker := time.NewTicker(8 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				sendTyping()
			}
		}
	}()
	return func() { close(done) }
}

func (b *Bot) audit(message string) {
	if b.logChannelID == "" || b.session == nil {
		return
	}
	if !b.allowAuditMessage() {
		return
	}
	if _, err := b.session.ChannelMessageSend(b.logChannelID, truncate(message, 1900)); err != nil {
		b.logger.Warn("send Discord audit log", "error", err)
	}
}

func (b *Bot) allowAuditMessage() bool {
	const maxPerMinute = 20
	now := time.Now()
	b.auditMu.Lock()
	defer b.auditMu.Unlock()
	if b.auditWindow.IsZero() || now.Sub(b.auditWindow) >= time.Minute {
		if b.auditDropped > 0 {
			b.logger.Warn("Discord audit messages dropped by local throttle", "count", b.auditDropped)
		}
		b.auditWindow = now
		b.auditSent = 0
		b.auditDropped = 0
	}
	if b.auditSent >= maxPerMinute {
		b.auditDropped++
		return false
	}
	b.auditSent++
	return true
}
func (b *Bot) respond(i *discordgo.InteractionCreate, text string, ephemeral bool) {
	flags := discordgo.MessageFlags(0)
	if ephemeral {
		flags = discordgo.MessageFlagsEphemeral
	}
	_ = b.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource, Data: &discordgo.InteractionResponseData{Content: truncate(text, 1900), Flags: flags}})
}
func interactionUser(i *discordgo.InteractionCreate) *discordgo.User {
	if i.Member != nil {
		return i.Member.User
	}
	return i.User
}
func subOptions(options []*discordgo.ApplicationCommandInteractionDataOption, name string) []*discordgo.ApplicationCommandInteractionDataOption {
	for _, o := range options {
		if o.Name == name {
			return o.Options
		}
	}
	return nil
}
func optionString(options []*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	for _, o := range options {
		if o.Name == name {
			return o.StringValue()
		}
	}
	return ""
}
func optionUser(s *discordgo.Session, options []*discordgo.ApplicationCommandInteractionDataOption, name string) *discordgo.User {
	for _, o := range options {
		if o.Name == name {
			return o.UserValue(s)
		}
	}
	return nil
}
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

func parseUserTime(value, timezone string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, err
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02 3:04 PM"} {
		if t, err := time.ParseInLocation(layout, value, loc); err == nil {
			if !t.After(now) {
				return time.Time{}, errors.New("time must be in the future")
			}
			return t, nil
		}
	}
	return time.Time{}, errors.New("use YYYY-MM-DD HH:MM, such as 2026-07-18 16:00")
}

func resolveID(ctx context.Context, store reminders.Store, creatorID, prefix string) (uuid.UUID, error) {
	if id, err := uuid.Parse(prefix); err == nil {
		return id, nil
	}
	items, err := store.List(ctx, reminders.ListFilter{CreatorID: creatorID, Limit: 100})
	if err != nil {
		return uuid.Nil, err
	}
	var found uuid.UUID
	for _, r := range items {
		if strings.HasPrefix(r.ID.String(), strings.ToLower(prefix)) {
			if found != uuid.Nil {
				return uuid.Nil, errors.New("ID prefix is ambiguous")
			}
			found = r.ID
		}
	}
	if found == uuid.Nil {
		return uuid.Nil, reminders.ErrNotFound
	}
	return found, nil
}

func decodeArguments(raw json.RawMessage, dst any) error {
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
		return json.Unmarshal([]byte(text), dst)
	}
	return json.Unmarshal(raw, dst)
}

func commandDefinitions() []*discordgo.ApplicationCommand {
	stringOption := func(name, description string, required bool) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: name, Description: description, Required: required}
	}
	userOption := func(name, description string, required bool) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionUser, Name: name, Description: description, Required: required}
	}
	create := &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "create", Description: "Create a reminder", Options: []*discordgo.ApplicationCommandOption{stringOption("title", "What to remember", true), stringOption("when", "YYYY-MM-DD HH:MM in the target user's timezone", true), userOption("user", "Discord user to ping", true)}}
	return []*discordgo.ApplicationCommand{
		{Name: "remind", Description: "Manage reminders", Options: []*discordgo.ApplicationCommandOption{create, {Type: discordgo.ApplicationCommandOptionSubCommand, Name: "list", Description: "List your reminders"}, {Type: discordgo.ApplicationCommandOptionSubCommand, Name: "edit", Description: "Edit a reminder", Options: []*discordgo.ApplicationCommandOption{stringOption("id", "Reminder ID or prefix", true), stringOption("title", "Updated title", true), stringOption("when", "Updated YYYY-MM-DD HH:MM", true)}}, {Type: discordgo.ApplicationCommandOptionSubCommand, Name: "cancel", Description: "Cancel a reminder", Options: []*discordgo.ApplicationCommandOption{stringOption("id", "Reminder ID or prefix", true)}}, {Type: discordgo.ApplicationCommandOptionSubCommand, Name: "complete", Description: "Complete a reminder", Options: []*discordgo.ApplicationCommandOption{stringOption("id", "Reminder ID or prefix", true)}}}},
		{Name: "reminders", Description: "Owner: list all current reminders"},
		{Name: "todo", Description: "Create a to-do reminder", Options: []*discordgo.ApplicationCommandOption{create}},
		{Name: "timezone", Description: "Set your timezone", Options: []*discordgo.ApplicationCommandOption{{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "set", Description: "Set an IANA timezone", Options: []*discordgo.ApplicationCommandOption{stringOption("name", "For example America/New_York", true)}}}},
		{Name: "chat", Description: "Manage AI conversation context", Options: []*discordgo.ApplicationCommandOption{{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "reset", Description: "Reset your context"}}},
		{Name: "dashboard", Description: "Get the TaskBot dashboard URL"},
		{Name: "privacy", Description: "Manage your stored data", Options: []*discordgo.ApplicationCommandOption{{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "delete-my-data", Description: "Delete your stored data"}}},
	}
}
