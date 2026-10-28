package audit

import (
	"context"
	"log/slog"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/obs"
)

type EventType string

const (
	LoginStarted         EventType = "login_started"
	LoginSucceeded       EventType = "login_succeeded"
	LoginFailed          EventType = "login_failed"
	ConsentGranted       EventType = "consent_granted"
	ConsentDenied        EventType = "consent_denied"
	ClientRegistered     EventType = "client_registered"
	TokenIssued          EventType = "token_issued"
	TokenRefreshed       EventType = "token_refreshed"
	RefreshReuseDetected EventType = "refresh_reuse_detected"
	SessionRevoked       EventType = "session_revoked"
	SessionExpired       EventType = "session_expired"
	MealieTokenRenewed   EventType = "mealie_renewed"
)

type Logger struct {
	logger *slog.Logger
}

func New(logger *slog.Logger) *Logger {
	return &Logger{logger: logger}
}

func (l *Logger) Event(ctx context.Context, ev EventType, attrs ...slog.Attr) {
	base := []slog.Attr{
		slog.String("log_type", "audit"),
		slog.String("request_id", obs.RequestIDFrom(ctx)),
		slog.String("event", string(ev)),
	}
	l.logger.LogAttrs(ctx, slog.LevelInfo, "audit", append(base, attrs...)...)
}
