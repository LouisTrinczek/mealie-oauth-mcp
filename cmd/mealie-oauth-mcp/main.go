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

	"github.com/louistrinczek/mealie-oauth-mcp/internal/app"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/config"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/healthcheck"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/obs"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
)

const healthcheckTimeout = 3 * time.Second

var (
	version  = "dev"
	errUsage = errors.New("unknown command")
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:])
	stop()

	if err != nil {
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	switch {
	case len(args) == 0:
		return serve(ctx)
	case len(args) == 2 && args[0] == "backup":
		return backup(ctx, args[1])
	case len(args) == 1 && args[0] == "healthcheck":
		return checkHealth(ctx)
	default:
		return logFailure(startupLogger(), "usage: mealie-oauth-mcp [backup <file> | healthcheck]", errUsage)
	}
}

func backup(ctx context.Context, file string) error {
	logger := startupLogger()
	if err := store.Backup(ctx, config.DataDir(os.LookupEnv), file); err != nil {
		return logFailure(logger, "backup failed", err)
	}
	logger.Info("backup written", slog.String("path", file))
	return nil
}

func checkHealth(ctx context.Context) error {
	client := &http.Client{Timeout: healthcheckTimeout}
	if err := healthcheck.Check(ctx, client, config.ListenAddr(os.LookupEnv)); err != nil {
		return logFailure(startupLogger(), "health check failed", err)
	}
	return nil
}

func serve(ctx context.Context) error {
	cfg, err := config.Load(os.LookupEnv, os.ReadFile)
	if err != nil {
		return logFailure(startupLogger(), "invalid configuration", err)
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

func startupLogger() *slog.Logger {
	return obs.NewLogger(os.Stderr, "json", slog.LevelInfo)
}
