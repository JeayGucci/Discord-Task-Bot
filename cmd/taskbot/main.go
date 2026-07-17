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

	"github.com/jmantheitguy/Discord-Task-Bot/internal/bot"
	"github.com/jmantheitguy/Discord-Task-Bot/internal/config"
	"github.com/jmantheitguy/Discord-Task-Bot/internal/dashboard"
	"github.com/jmantheitguy/Discord-Task-Bot/internal/database"
	ai "github.com/jmantheitguy/Discord-Task-Bot/internal/openai"
	"github.com/jmantheitguy/Discord-Task-Bot/internal/scheduler"
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

	openAI := ai.New(cfg.OpenAIAPIKey, cfg.OpenAIModel, cfg.OpenAIBaseURL)
	var discordBot *bot.Bot
	if cfg.DiscordToken != "" {
		discordBot, err = bot.New(cfg.DiscordToken, cfg.DiscordAppID, cfg.DiscordGuildID, cfg.DiscordOwnerID, cfg.DiscordLogChannelID, cfg.DashboardBaseURL, store, openAI, logger)
		if err != nil {
			return err
		}
		if err := discordBot.Open(ctx, cfg.DiscordRegisterCommands); err != nil {
			return err
		}
		defer discordBot.Close()
		go scheduler.New(store, discordBot, cfg.SchedulerInterval, cfg.ClaimLimit, logger).Run(ctx)
	} else {
		logger.Warn("Discord is not configured; bot and scheduler are disabled")
	}

	server := &http.Server{Addr: cfg.Address(), Handler: dashboard.New(store, store, store, cfg.DashboardUsername, cfg.DashboardPasswordHash, cfg.DashboardPassword, cfg.DiscordOwnerID, cfg.DefaultTimezone, logger).Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
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
