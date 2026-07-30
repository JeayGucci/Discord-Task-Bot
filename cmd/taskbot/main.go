package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JeayGucci/Discord-Task-Bot/internal/bot"
	"github.com/JeayGucci/Discord-Task-Bot/internal/config"
	"github.com/JeayGucci/Discord-Task-Bot/internal/dashboard"
	"github.com/JeayGucci/Discord-Task-Bot/internal/database"
	ai "github.com/JeayGucci/Discord-Task-Bot/internal/openai"
	"github.com/JeayGucci/Discord-Task-Bot/internal/ops"
	"github.com/JeayGucci/Discord-Task-Bot/internal/scheduler"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("taskbot stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	store, err := database.Open(ctx, cfg.DatabasePublicURL, cfg.DefaultTimezone)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		return err
	}

	recorder := ops.NewRecorder(300)
	openAI := ai.New(cfg.OpenAIAPIKey, cfg.OpenAIModel, cfg.OpenAIBaseURL)
	recorder.SetHealth("openai_configured", openAI.Enabled())
	recorder.SetHealth("discord_configured", cfg.DiscordToken != "")
	var discordBot *bot.Bot
	if cfg.DiscordToken != "" {
		discordBot, err = bot.New(cfg.DiscordToken, cfg.DiscordAppID, cfg.DiscordGuildID, cfg.DiscordOwnerID, cfg.DiscordReminderChannelID, cfg.DiscordStreamURL, cfg.DashboardBaseURL, store, openAI, logger, recorder)
		if err != nil {
			return err
		}
		if err := discordBot.Open(ctx, cfg.DiscordRegisterCommands); err != nil {
			return err
		}
		defer discordBot.Close()
		go scheduler.New(store, discordBot, cfg.SchedulerInterval, cfg.ClaimLimit, logger, recorder).Run(ctx)
	} else {
		logger.Warn("Discord is not configured; bot and scheduler are disabled")
		recorder.SetHealth("discord_connected", false)
		recorder.SetHealth("scheduler_status", "disabled")
	}

	server := &http.Server{Addr: cfg.Address(), Handler: dashboard.New(store, store, store, discordBot, cfg.DashboardUsername, cfg.DashboardPasswordHash, cfg.DashboardPassword, cfg.DiscordOwnerID, cfg.DiscordReminderChannelID, cfg.DefaultTimezone, logger, recorder).Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	errCh := make(chan error, 1)
	go func() { logger.Info("dashboard listening", "address", cfg.Address()); errCh <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
