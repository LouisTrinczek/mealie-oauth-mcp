package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/app"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/config"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/obs"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx)
	stop()

	if err != nil {
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.Load(os.LookupEnv, os.ReadFile)
	if err != nil {
		return logFailure(obs.NewLogger(os.Stderr, "json", slog.LevelInfo), "invalid configuration", err)
	}
	logger := obs.NewLogger(os.Stderr, cfg.LogFormat, cfg.LogLevel)
	a, err := app.New(ctx, cfg, logger, version)
	if err != nil {
		return logFailure(logger, "startup failed", err)
	}
	if err := errors.Join(a.Run(ctx), a.Close()); err != nil {
		return logFailure(logger, "server stopped with an error", err)
	}
	return nil
}

func logFailure(logger *slog.Logger, msg string, err error) error {
	logger.Error(msg, slog.Any("error", err))
	return err
}
