package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

type Config struct {
	Environment              string
	Port                     string
	DatabasePublicURL        string
	DiscordToken             string
	DiscordAppID             string
	DiscordGuildID           string
	DiscordOwnerID           string
	DiscordLogChannelID      string
	DiscordReminderChannelID string
	DiscordRegisterCommands  bool
	DiscordStreamURL         string
	OpenAIAPIKey             string
	OpenAIModel              string
	OpenAIBaseURL            string
	DashboardBaseURL         string
	DashboardUsername        string
	DashboardPasswordHash    string
	DashboardPassword        string
	DefaultTimezone          string
	SchedulerInterval        time.Duration
	ClaimLimit               int
}

func Load() (Config, error) {
	c := Config{
		Environment:              value("APP_ENV", "development"),
		Port:                     value("PORT", "8080"),
		DatabasePublicURL:        os.Getenv("DATABASE_PUBLIC_URL"),
		DiscordToken:             os.Getenv("DISCORD_BOT_TOKEN"),
		DiscordAppID:             os.Getenv("DISCORD_APPLICATION_ID"),
		DiscordGuildID:           os.Getenv("DISCORD_GUILD_ID"),
		DiscordOwnerID:           os.Getenv("DISCORD_OWNER_ID"),
		DiscordLogChannelID:      value("DISCORD_LOG_CHANNEL_ID", "1526851837221671043"),
		DiscordReminderChannelID: value("DISCORD_REMINDER_CHANNEL_ID", "1526792654044532756"),
		DiscordRegisterCommands:  boolean("DISCORD_REGISTER_COMMANDS", os.Getenv("APP_ENV") != "production"),
		DiscordStreamURL:         os.Getenv("DISCORD_STREAM_URL"),
		OpenAIAPIKey:             os.Getenv("OPENAI_API_KEY"),
		OpenAIModel:              value("OPENAI_CHAT_MODEL", "gpt-5-nano"),
		OpenAIBaseURL:            value("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		DashboardBaseURL:         os.Getenv("DASHBOARD_BASE_URL"),
		DashboardUsername:        value("DASHBOARD_USERNAME", "admin"),
		DashboardPasswordHash:    os.Getenv("DASHBOARD_PASSWORD_HASH"),
		DashboardPassword:        os.Getenv("DASHBOARD_PASSWORD"),
		DefaultTimezone:          value("DEFAULT_TIMEZONE", "America/New_York"),
		SchedulerInterval:        duration("SCHEDULER_INTERVAL", 15*time.Second),
		ClaimLimit:               integer("SCHEDULER_CLAIM_LIMIT", 25),
	}

	if _, err := time.LoadLocation(c.DefaultTimezone); err != nil {
		return Config{}, fmt.Errorf("DEFAULT_TIMEZONE: %w", err)
	}
	if c.DatabasePublicURL == "" {
		return Config{}, errors.New("DATABASE_PUBLIC_URL is required")
	}
	if (c.DiscordToken == "") != (c.DiscordAppID == "") {
		return Config{}, errors.New("DISCORD_BOT_TOKEN and DISCORD_APPLICATION_ID must be configured together")
	}
	if c.Environment == "production" && c.DashboardPasswordHash == "" && c.DashboardPassword == "" {
		return Config{}, errors.New("DASHBOARD_PASSWORD_HASH or DASHBOARD_PASSWORD is required in production")
	}
	return c, nil
}

func (c Config) Address() string { return ":" + strings.TrimPrefix(c.Port, ":") }

func value(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func duration(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func integer(key string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func boolean(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "":
		return fallback
	case "1", "true", "t", "yes", "y", "on":
		return true
	case "0", "false", "f", "no", "n", "off":
		return false
	default:
		return fallback
	}
}
