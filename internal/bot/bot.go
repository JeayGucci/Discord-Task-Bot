package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/google/uuid"
	"github.com/jmantheitguy/Discord-Task-Bot/internal/channels"
	ai "github.com/jmantheitguy/Discord-Task-Bot/internal/openai"
	"github.com/jmantheitguy/Discord-Task-Bot/internal/ops"
	"github.com/jmantheitguy/Discord-Task-Bot/internal/reminders"
)

type Bot struct {
	session           *discordgo.Session
	store             reminders.Store
	ai                *ai.Client
	appID             string
	guildID           string
	ownerID           string
	reminderChannelID string
	streamURL         string
	dashboardURL      string
	logger            *slog.Logger
	recorder          *ops.Recorder
	commands          []*discordgo.ApplicationCommand
	reminderMu        sync.Mutex
	nextReminder      time.Time
}

const fixedTimezone = "America/New_York"

var relativeReminderPattern = regexp.MustCompile(`\bin\s+([a-z0-9]+)\s+(minute|minutes|min|mins|hour|hours|hr|hrs|day|days|week|weeks)\b`)

func New(token, appID, guildID, ownerID, reminderChannelID, streamURL, dashboardURL string, store reminders.Store, aiClient *ai.Client, logger *slog.Logger, recorder *ops.Recorder) (*Bot, error) {
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	s.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentMessageContent
	b := &Bot{session: s, store: store, ai: aiClient, appID: appID, guildID: guildID, ownerID: ownerID, reminderChannelID: reminderChannelID, streamURL: streamURL, dashboardURL: dashboardURL, logger: logger, recorder: recorder, commands: commandDefinitions()}
	s.AddHandler(b.onInteraction)
	s.AddHandler(b.onMessage)
	return b, nil
}

func (b *Bot) Open(ctx context.Context, registerCommands bool) error {
	if err := b.session.Open(); err != nil {
		return err
	}
	b.recorder.SetHealth("discord_connected", true)
	b.recorder.SetHealth("discord_guild_scoped", b.guildID != "")
	if registerCommands {
		if _, err := b.session.ApplicationCommandBulkOverwrite(b.appID, b.guildID, b.commands); err != nil {
			b.session.Close()
			b.recorder.SetHealth("discord_connected", false)
			return fmt.Errorf("register commands: %w", err)
		}
	}
	b.logger.Info("discord bot connected", "guild_scoped", b.guildID != "", "register_commands", registerCommands)
	b.recorder.Record("info", "discord", "bot connected", ops.Attributes("guild_scoped", b.guildID != "", "register_commands", registerCommands))
	if b.streamURL != "" {
		if !isDiscordStreamingURL(b.streamURL) {
			b.logger.Warn("Discord streaming presence URL should be a Twitch or YouTube URL", "url", b.streamURL)
			b.recorder.Record("warn", "discord", "streaming presence URL should be Twitch or YouTube", ops.Attributes("url", b.streamURL))
		}
		if err := b.session.UpdateStreamingStatus(0, "Streamlining your tasks", b.streamURL); err != nil {
			b.logger.Warn("set Discord streaming presence", "error", err)
			b.recorder.Record("warn", "discord", "set streaming presence failed", ops.Attributes("error", err.Error()))
		} else {
			b.recorder.SetHealth("discord_presence", "streaming")
		}
	} else {
		b.logger.Warn("Discord streaming presence is disabled; set DISCORD_STREAM_URL to a Twitch or YouTube URL")
		b.recorder.Record("warn", "discord", "streaming presence is disabled", nil)
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
	channelID := strings.TrimSpace(r.ChannelID)
	if channelID == "" {
		channelID = b.reminderChannelID
	}
	message, err := b.session.ChannelMessageSend(channelID, content)
	if err != nil {
		b.recorder.Record("error", "reminder", "delivery failed", ops.Attributes("reminder_id", r.ID, "channel_id", channelID, "error", err.Error()))
		return "", err
	}
	b.recorder.Record("info", "reminder", "delivered", ops.Attributes("reminder_id", r.ID, "channel_id", channelID, "discord_message_id", message.ID))
	return message.ID, nil
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

func (b *Bot) ListChannels(ctx context.Context) ([]channels.Group, error) {
	if strings.TrimSpace(b.guildID) == "" {
		return nil, errors.New("DISCORD_GUILD_ID is required to list channels")
	}
	guildChannels, err := b.session.GuildChannels(b.guildID, discordgo.WithContext(ctx))
	if err != nil {
		b.recorder.SetHealth("channel_list_status", "error")
		b.recorder.SetHealth("channel_list_error", err.Error())
		b.recorder.Record("warn", "discord", "list channels failed", ops.Attributes("error", err.Error()))
		return nil, err
	}
	b.recorder.SetHealth("channel_list_status", "ok")
	b.recorder.SetHealth("channel_list_count", len(guildChannels))
	b.recorder.SetHealth("channel_list_checked_at", time.Now().UTC())
	categories := map[string]*discordgo.Channel{}
	textByParent := map[string][]*discordgo.Channel{}
	for _, channel := range guildChannels {
		switch channel.Type {
		case discordgo.ChannelTypeGuildCategory:
			categories[channel.ID] = channel
		case discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews:
			textByParent[channel.ParentID] = append(textByParent[channel.ParentID], channel)
		}
	}
	sortChannels := func(items []*discordgo.Channel) {
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].Position == items[j].Position {
				return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
			}
			return items[i].Position < items[j].Position
		})
	}
	toChannels := func(items []*discordgo.Channel) []channels.Channel {
		sortChannels(items)
		result := make([]channels.Channel, 0, len(items))
		for _, item := range items {
			result = append(result, channels.Channel{ID: item.ID, Name: item.Name})
		}
		return result
	}
	result := make([]channels.Group, 0)
	if uncategorized := toChannels(textByParent[""]); len(uncategorized) > 0 {
		result = append(result, channels.Group{Name: "Uncategorized", Channels: uncategorized})
	}
	categoryList := make([]*discordgo.Channel, 0, len(categories))
	for _, category := range categories {
		categoryList = append(categoryList, category)
	}
	sortChannels(categoryList)
	for _, category := range categoryList {
		groupChannels := toChannels(textByParent[category.ID])
		if len(groupChannels) == 0 {
			continue
		}
		result = append(result, channels.Group{ID: category.ID, Name: category.Name, Channels: groupChannels})
	}
	return result, nil
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
		b.recorder.Record("error", "discord", "command failed", ops.Attributes("command", data.Name, "user_id", user.ID, "guild_id", i.GuildID, "channel_id", i.ChannelID, "error", err.Error()))
		message = "I couldn't complete that request: " + err.Error()
	} else {
		b.logger.Info("discord command completed", "command", data.Name, "user_id", user.ID, "guild_id", i.GuildID, "channel_id", i.ChannelID)
		b.recorder.Record("info", "discord", "command completed", ops.Attributes("command", data.Name, "user_id", user.ID, "guild_id", i.GuildID, "channel_id", i.ChannelID))
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
			lines = append(lines, fmt.Sprintf("• `%s` <@%s> in <#%s> **%s** — <t:%d:F> (%s)", r.ID.String()[:8], r.CreatorID, r.ChannelID, r.Title, r.DeliveryAt.Unix(), r.Status))
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
		delivery, err := parseUserTime(optionString(options[0].Options, "when"), fixedTimezone, time.Now())
		if err != nil {
			return "", err
		}
		updated, err := b.store.Update(ctx, reminders.UpdateParams{ID: id, CreatorID: interactionUser(i).ID, Title: optionString(options[0].Options, "title"), DeliveryAt: delivery, Timezone: fixedTimezone})
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
		lines = append(lines, fmt.Sprintf("• `%s` <@%s> in <#%s> **%s** — <t:%d:F> (%s)", r.ID.String()[:8], r.CreatorID, r.ChannelID, r.Title, r.DeliveryAt.Unix(), r.Status))
	}
	return strings.Join(lines, "\n"), nil
}

func (b *Bot) createFromOptions(ctx context.Context, i *discordgo.InteractionCreate, options []*discordgo.ApplicationCommandInteractionDataOption) (string, error) {
	target := optionUser(b.session, options, "user")
	if target == nil {
		return "", errors.New("choose the Discord user to ping")
	}
	channelID := optionChannelID(options, "channel")
	if channelID == "" {
		return "", errors.New("choose the Discord channel for the reminder")
	}
	delivery, err := parseUserTime(optionString(options, "when"), fixedTimezone, time.Now())
	if err != nil {
		return "", err
	}
	r, err := b.store.Create(ctx, reminders.CreateParams{Title: optionString(options, "title"), CreatorID: target.ID, GuildID: i.GuildID, ChannelID: channelID, MentionTarget: "<@" + target.ID + ">", DeliveryAt: delivery, Timezone: fixedTimezone})
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
	b.recorder.Record("info", "reminder", "slash reminder created", ops.Attributes("reminder_id", r.ID, "title", r.Title, "user_id", target.ID, "guild_id", i.GuildID, "channel_id", channelID, "delivery_at", r.DeliveryAt, "timezone", r.Timezone))
	b.sendCreationConfirmation(channelID, fmt.Sprintf("Created reminder **%s** for <@%s> at <t:%d:F>. ID: `%s`", r.Title, target.ID, r.DeliveryAt.Unix(), r.ID.String()[:8]))
	return fmt.Sprintf("Created **%s** for <@%s> in <#%s> at <t:%d:F>. ID: `%s`", r.Title, target.ID, channelID, r.DeliveryAt.Unix(), r.ID.String()[:8]), nil
}

func (b *Bot) handleTimezone(ctx context.Context, userID string, options []*discordgo.ApplicationCommandInteractionDataOption) (string, error) {
	return "TaskBot uses **America/New_York** for all reminders.", nil
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
	requestNow := time.Now()
	previousID, err := b.store.GetConversation(ctx, m.Author.ID, m.ChannelID)
	if err != nil {
		b.logger.Warn("load conversation", "error", err)
	}
	forceTool := ""
	if looksLikeReminderRequest(content) {
		forceTool = "create_reminder"
	}
	result, err := b.ai.Respond(ctx, content, ai.Context{Now: requestNow, Timezone: fixedTimezone, UserID: m.Author.ID, GuildID: m.GuildID, ChannelID: m.ChannelID, PreviousResponseID: previousID, ForceTool: forceTool})
	if err != nil && previousID != "" && strings.Contains(err.Error(), "No tool output found") {
		b.logger.Warn("reset stale AI conversation after missing tool output", "user_id", m.Author.ID, "guild_id", m.GuildID, "channel_id", m.ChannelID, "previous_response_id", previousID)
		b.recorder.Record("warn", "ai", "reset stale AI conversation after missing tool output", ops.Attributes("user_id", m.Author.ID, "guild_id", m.GuildID, "channel_id", m.ChannelID, "previous_response_id", previousID))
		if resetErr := b.store.ResetConversation(ctx, m.Author.ID, m.ChannelID); resetErr != nil {
			b.logger.Warn("reset stale AI conversation", "error", resetErr)
		}
		result, err = b.ai.Respond(ctx, content, ai.Context{Now: requestNow, Timezone: fixedTimezone, UserID: m.Author.ID, GuildID: m.GuildID, ChannelID: m.ChannelID, ForceTool: forceTool})
	}
	if err != nil {
		b.logger.Error("AI response", "error", err)
		b.recorder.Record("error", "ai", "AI request failed", ops.Attributes("user_id", m.Author.ID, "guild_id", m.GuildID, "channel_id", m.ChannelID, "error", err.Error()))
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
	b.recorder.Record("info", "ai", "AI response received", ops.Attributes("user_id", m.Author.ID, "guild_id", m.GuildID, "channel_id", m.ChannelID, "response_id", result.ResponseID, "has_tool_action", result.Action != nil, "text", truncate(result.Text, 1000)))
	if result.Action == nil {
		if result.ResponseID != "" {
			if err := b.store.SaveConversation(ctx, m.Author.ID, m.GuildID, m.ChannelID, result.ResponseID, time.Now().Add(7*24*time.Hour)); err != nil {
				b.logger.Warn("save conversation", "error", err)
			}
		}
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
	b.recorder.Record("info", "ai", "AI tool action requested", ops.Attributes("user_id", m.Author.ID, "guild_id", m.GuildID, "channel_id", m.ChannelID, "tool", result.Action.Name, "arguments", truncate(string(result.Action.Arguments), 1000)))
	if result.Action.Name == "get_bot_status" {
		b.reply(m, b.botStatusMessage())
		return
	}
	if result.Action.Name != "create_reminder" {
		b.logger.Warn("unsupported AI tool action", "tool", result.Action.Name, "user_id", m.Author.ID, "guild_id", m.GuildID, "channel_id", m.ChannelID)
		b.recorder.Record("warn", "ai", "unsupported AI tool action", ops.Attributes("tool", result.Action.Name, "user_id", m.Author.ID, "guild_id", m.GuildID, "channel_id", m.ChannelID))
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
	if forceTool == "create_reminder" {
		if inferred, inferErr := parseRelativeReminderTime(content, fixedTimezone, requestNow); inferErr == nil {
			delivery = inferred
			err = nil
			b.recorder.Record("info", "reminder", "used deterministic relative reminder time", ops.Attributes("user_id", m.Author.ID, "guild_id", m.GuildID, "channel_id", m.ChannelID, "content", truncate(content, 200), "delivery_at", delivery))
		}
	}
	if err != nil || !delivery.After(requestNow) {
		b.reply(m, "I need an exact date and time before creating that reminder.")
		return
	}
	channelID := strings.TrimSpace(b.reminderChannelID)
	if channelID == "" {
		channelID = m.ChannelID
	}
	r, err := b.store.Create(ctx, reminders.CreateParams{Title: args.Title, Description: args.Description, CreatorID: m.Author.ID, GuildID: m.GuildID, ChannelID: channelID, MentionTarget: "<@" + m.Author.ID + ">", DeliveryAt: delivery, Timezone: fixedTimezone})
	if err != nil {
		b.logger.Error("AI reminder creation failed", "error", err, "user_id", m.Author.ID, "guild_id", m.GuildID, "channel_id", m.ChannelID)
		b.recorder.Record("error", "reminder", "AI reminder creation failed", ops.Attributes("error", err.Error(), "user_id", m.Author.ID, "guild_id", m.GuildID, "channel_id", m.ChannelID))
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
	b.recorder.Record("info", "reminder", "AI reminder created", ops.Attributes("reminder_id", r.ID, "title", r.Title, "user_id", m.Author.ID, "guild_id", m.GuildID, "channel_id", channelID, "delivery_at", r.DeliveryAt, "timezone", r.Timezone))
	b.sendCreationConfirmation(channelID, fmt.Sprintf("Created reminder **%s** for <@%s> at <t:%d:F>. ID: `%s`", r.Title, m.Author.ID, r.DeliveryAt.Unix(), r.ID.String()[:8]))
	b.reply(m, fmt.Sprintf("Created **%s** for <t:%d:F>. ID: `%s`", r.Title, r.DeliveryAt.Unix(), r.ID.String()[:8]))
}

func looksLikeReminderRequest(content string) bool {
	text := strings.ToLower(content)
	if !strings.Contains(text, "remind") && !strings.Contains(text, "reminder") {
		return false
	}
	timeHints := []string{
		" in ", " at ", " on ", " by ", " tomorrow", " today", " tonight", " next ",
		"minute", "hour", "day", "week", "month", "am", "pm", ":",
	}
	for _, hint := range timeHints {
		if strings.Contains(text, hint) {
			return true
		}
	}
	return false
}

func parseRelativeReminderTime(content, timezone string, now time.Time) (time.Time, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, err
	}
	match := relativeReminderPattern.FindStringSubmatch(strings.ToLower(content))
	if len(match) != 3 {
		return time.Time{}, errors.New("no relative time found")
	}
	amount, err := parseSmallNumber(match[1])
	if err != nil {
		return time.Time{}, err
	}
	var d time.Duration
	switch match[2] {
	case "minute", "minutes", "min", "mins":
		d = time.Duration(amount) * time.Minute
	case "hour", "hours", "hr", "hrs":
		d = time.Duration(amount) * time.Hour
	case "day", "days":
		d = time.Duration(amount) * 24 * time.Hour
	case "week", "weeks":
		d = time.Duration(amount) * 7 * 24 * time.Hour
	default:
		return time.Time{}, errors.New("unsupported relative time unit")
	}
	if d <= 0 {
		return time.Time{}, errors.New("relative time must be positive")
	}
	return now.In(loc).Add(d), nil
}

func parseSmallNumber(value string) (int, error) {
	if n, err := strconv.Atoi(value); err == nil {
		return n, nil
	}
	words := map[string]int{
		"a": 1, "an": 1, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
		"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11,
		"twelve": 12,
	}
	if n, ok := words[value]; ok {
		return n, nil
	}
	return 0, errors.New("unsupported number")
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

func (b *Bot) sendCreationConfirmation(channelID, text string) {
	if strings.TrimSpace(channelID) == "" {
		return
	}
	if _, err := b.session.ChannelMessageSend(channelID, truncate(text, 1900)); err != nil {
		b.logger.Warn("send reminder creation confirmation", "channel_id", channelID, "error", err)
		b.recorder.Record("warn", "discord", "send reminder creation confirmation failed", ops.Attributes("channel_id", channelID, "error", err.Error()))
	}
}

func (b *Bot) botStatusMessage() string {
	channelStatus := "unknown"
	defaultChannelStatus := "unknown"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if groups, err := b.ListChannels(ctx); err != nil {
		channelStatus = "error: " + truncate(err.Error(), 160)
		defaultChannelStatus = "not checked"
	} else {
		count := 0
		defaultVisible := false
		for _, group := range groups {
			for _, channel := range group.Channels {
				count++
				if strings.EqualFold(strings.TrimSpace(channel.Name), "general-to-do-list") {
					defaultVisible = true
				}
			}
		}
		channelStatus = fmt.Sprintf("ok (%d accessible text/news channels)", count)
		if defaultVisible {
			defaultChannelStatus = "visible"
		} else {
			defaultChannelStatus = "not visible"
		}
	}
	health := b.recorder.Health()
	read := func(key string) string {
		if value, ok := health[key]; ok {
			return fmt.Sprint(value)
		}
		return "unknown"
	}
	lines := []string{
		"TaskBot status:",
		"• Discord connected: " + read("discord_connected"),
		"• Scheduler: " + read("scheduler_status"),
		"• OpenAI configured: " + read("openai_configured"),
		"• Channel list: " + channelStatus,
		"• Default dashboard user: Jeay",
		"• Default dashboard channel: #general-to-do-list (" + defaultChannelStatus + ")",
		"• Dashboard: " + b.dashboardMessage(),
		"",
		"I can create reminders, list/edit/cancel/complete reminders with slash commands, answer simple questions, show this sanitized status, and keep operational logs in Railway plus the admin dashboard.",
	}
	return truncate(strings.Join(lines, "\n"), 1900)
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
func optionChannelID(options []*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	for _, o := range options {
		if o.Name == name {
			if channel := o.ChannelValue(nil); channel != nil {
				return channel.ID
			}
		}
	}
	return ""
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
	channelOption := func(name, description string, required bool) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionChannel, Name: name, Description: description, Required: required, ChannelTypes: []discordgo.ChannelType{discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews}}
	}
	create := &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "create", Description: "Create a reminder", Options: []*discordgo.ApplicationCommandOption{stringOption("title", "What to remember", true), stringOption("when", "YYYY-MM-DD HH:MM in America/New_York", true), userOption("user", "Discord user to ping", true), channelOption("channel", "Channel where the reminder should post", true)}}
	return []*discordgo.ApplicationCommand{
		{Name: "remind", Description: "Manage reminders", Options: []*discordgo.ApplicationCommandOption{create, {Type: discordgo.ApplicationCommandOptionSubCommand, Name: "list", Description: "List your reminders"}, {Type: discordgo.ApplicationCommandOptionSubCommand, Name: "edit", Description: "Edit a reminder", Options: []*discordgo.ApplicationCommandOption{stringOption("id", "Reminder ID or prefix", true), stringOption("title", "Updated title", true), stringOption("when", "Updated YYYY-MM-DD HH:MM", true)}}, {Type: discordgo.ApplicationCommandOptionSubCommand, Name: "cancel", Description: "Cancel a reminder", Options: []*discordgo.ApplicationCommandOption{stringOption("id", "Reminder ID or prefix", true)}}, {Type: discordgo.ApplicationCommandOptionSubCommand, Name: "complete", Description: "Complete a reminder", Options: []*discordgo.ApplicationCommandOption{stringOption("id", "Reminder ID or prefix", true)}}}},
		{Name: "reminders", Description: "Owner: list all current reminders"},
		{Name: "todo", Description: "Create a to-do reminder", Options: []*discordgo.ApplicationCommandOption{create}},
		{Name: "chat", Description: "Manage AI conversation context", Options: []*discordgo.ApplicationCommandOption{{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "reset", Description: "Reset your context"}}},
		{Name: "dashboard", Description: "Get the TaskBot dashboard URL"},
		{Name: "privacy", Description: "Manage your stored data", Options: []*discordgo.ApplicationCommandOption{{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "delete-my-data", Description: "Delete your stored data"}}},
	}
}
