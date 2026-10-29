package oauth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/audit"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/token"
)

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	if !parseTokenForm(w, r) {
		return
	}
	ctx := r.Context()
	sessionID, err := s.sessionOfToken(ctx, r.PostForm.Get("token"))
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		s.tokenServerError(w, r, "looking up token to revoke", err)
		return
	}
	if err := s.store.RevokeSession(ctx, sessionID, s.now()); err != nil {
		s.tokenServerError(w, r, "revoking session", err)
		return
	}
	s.audit.Event(ctx, audit.SessionRevoked, slog.String("session_id", sessionID), slog.String("reason", "client_revocation"))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) sessionOfToken(ctx context.Context, plain string) (string, error) {
	hash := token.Hash(plain)
	switch {
	case token.HasKind(plain, token.Access):
		access, err := s.store.GetAccessToken(ctx, hash)
		return access.SessionID, err
	case token.HasKind(plain, token.Refresh):
		refresh, err := s.store.GetRefreshToken(ctx, hash)
		return refresh.SessionID, err
	default:
		return "", store.ErrNotFound
	}
}
