package oauth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/audit"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/token"
)

var errMealieRefused = errors.New("mealie refused the login")

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	if q.Get("state") == "" || (q.Get("code") == "" && q.Get("error") == "") {
		s.renderError(w, r, http.StatusBadRequest, msgExpired)
		return
	}
	pending, err := s.store.GetPendingAuthByUpstreamState(ctx, token.Hash(q.Get("state")))
	if errors.Is(err, store.ErrNotFound) {
		s.renderError(w, r, http.StatusBadRequest, msgExpired)
		return
	}
	if err != nil {
		s.internalError(w, r, "getting pending auth", err)
		return
	}
	pendingID, binding, ok := s.readBinding(r)
	if !ok || pendingID != pending.ID || !bindingMatches(binding, pending) {
		s.audit.Event(ctx, audit.LoginFailed, slog.String("client_id", pending.ClientID), slog.String("reason", "browser_binding_mismatch"))
		s.renderError(w, r, http.StatusBadRequest, msgWrongBrowser)
		return
	}
	if !s.claimPending(w, r, pending.ID) {
		return
	}
	if q.Get("error") != "" {
		s.abandon(ctx, pending, slog.String("reason", "upstream_error"))
		s.redirectError(w, r, pending.RedirectURI, pending.ClientState, upstreamErrorCode(q.Get("error")), "The identity provider did not complete the login.")
		return
	}
	tok, err := s.mealie.NativeToken(ctx, mealie.NativeTokenRequest{
		Code:         secret.FromString(q.Get("code")),
		CodeVerifier: pending.UpstreamVerifier,
		RedirectURI:  s.cfg.CallbackURL,
		Nonce:        pending.UpstreamNonce,
	})
	user, err := s.resolveMealieLogin(ctx, tok, err)
	if err != nil {
		s.failMealieLogin(w, r, pending, err, msgMealieRefused, msgMealieFailed)
		return
	}
	s.completeLogin(w, r, pending, binding, tok, user)
}

func (s *Server) claimPending(w http.ResponseWriter, r *http.Request, id string) bool {
	err := s.store.ClaimPendingAuth(r.Context(), id, s.now())
	if errors.Is(err, store.ErrNotFound) {
		s.renderError(w, r, http.StatusBadRequest, msgExpired)
	} else if err != nil {
		s.internalError(w, r, "claiming pending auth", err)
	}
	return err == nil
}

func (s *Server) completeLogin(w http.ResponseWriter, r *http.Request, pending store.PendingAuth, binding string, tok mealie.Token, user mealie.User) {
	ctx := r.Context()
	pending, err := s.saveLogin(ctx, pending, tok, user)
	if err != nil {
		s.internalError(w, r, "saving login", err)
		return
	}
	consented, err := s.store.HasConsent(ctx, pending.MealieUserID, pending.ClientID)
	if err != nil {
		s.internalError(w, r, "checking consent", err)
		return
	}
	if consented && r.Method == http.MethodGet {
		s.finish(w, r, pending)
		return
	}
	s.showConsent(w, r, pending, binding, consented)
}

func (s *Server) saveLogin(ctx context.Context, pending store.PendingAuth, tok mealie.Token, user mealie.User) (store.PendingAuth, error) {
	sealed := s.keyring.Seal(tok.AccessToken.Bytes(), pendingAAD(pending.ID))
	login := store.Login{
		MealieUserID:         user.ID,
		MealieUsername:       user.Username,
		MealieTokenCT:        sealed,
		MealieTokenExpiresAt: s.now().Add(tok.ExpiresIn),
		MealieTokenLifetime:  tok.ExpiresIn,
	}
	if err := s.store.SetPendingAuthLogin(ctx, pending.ID, login); err != nil {
		return pending, err
	}
	pending.MealieUserID = login.MealieUserID
	pending.MealieUsername = login.MealieUsername
	pending.MealieTokenCT = login.MealieTokenCT
	pending.MealieTokenExpiresAt = login.MealieTokenExpiresAt
	pending.MealieTokenLifetime = login.MealieTokenLifetime
	return pending, nil
}

func (s *Server) resolveMealieLogin(ctx context.Context, tok mealie.Token, err error) (mealie.User, error) {
	if err != nil {
		return mealie.User{}, err
	}
	if len(tok.AccessToken.Bytes()) == 0 || tok.ExpiresIn <= 0 {
		return mealie.User{}, errMealieRefused
	}
	user, err := s.mealie.Self(ctx, tok.AccessToken)
	if err != nil {
		return mealie.User{}, err
	}
	if user.ID == "" {
		return mealie.User{}, errMealieRefused
	}
	return user, nil
}

func (s *Server) failMealieLogin(w http.ResponseWriter, r *http.Request, pending store.PendingAuth, err error, refusedMessage, failedMessage string) {
	refused := errors.Is(err, errMealieRefused) || mealie.IsUnauthorized(err)
	reason, status, message := "mealie_unavailable", http.StatusBadGateway, failedMessage
	if refused {
		reason, status, message = "mealie_refused", http.StatusForbidden, refusedMessage
	}
	s.abandon(r.Context(), pending, append([]slog.Attr{slog.String("reason", reason)}, mealieStatus(err)...)...)
	s.renderError(w, r, status, message)
}

func (s *Server) abandon(ctx context.Context, pending store.PendingAuth, attrs ...slog.Attr) {
	if err := s.store.DeletePendingAuth(ctx, pending.ID); err != nil {
		s.logger.WarnContext(ctx, "deleting pending auth", slog.String("error", err.Error()))
	}
	s.audit.Event(ctx, audit.LoginFailed, append([]slog.Attr{slog.String("client_id", pending.ClientID)}, attrs...)...)
}

func upstreamErrorCode(code string) string {
	switch code {
	case "invalid_request", "unauthorized_client", "access_denied", "unsupported_response_type",
		"invalid_scope", "server_error", "temporarily_unavailable":
		return code
	default:
		return "access_denied"
	}
}

func pendingAAD(pendingID string) []byte {
	return []byte("pending:" + pendingID)
}
