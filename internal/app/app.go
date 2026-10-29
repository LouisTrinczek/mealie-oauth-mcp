package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/audit"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/config"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/httpx"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/mcpserver"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/oauth"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/obs"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/session"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/tokencrypt"
)

const (
	readHeaderTimeout   = 10 * time.Second
	readTimeout         = 30 * time.Second
	writeTimeout        = 0
	idleTimeout         = 120 * time.Second
	maxHeaderBytes      = 64 << 10
	shutdownTimeout     = 20 * time.Second
	shutdownGracePeriod = 5 * time.Second

	mcpSessionTimeout   = 30 * time.Minute
	mcpMaxRequestBytes  = 4 << 20
	mcpScope            = "mealie"
	resourceMetadataURI = "/.well-known/oauth-protected-resource/mcp"

	readyTimeout    = 2 * time.Second
	cleanupInterval = 10 * time.Minute

	startupRetries    = 5
	startupRetryDelay = 6 * time.Second
	startupReachLimit = 30 * time.Second
	userAgentPrefix   = "mealie-oauth-mcp/"
)

type App struct {
	cfg           *config.Config
	logger        *slog.Logger
	store         *store.Store
	mealie        *mealie.Client
	now           func() time.Time
	handler       http.Handler
	shutdownGrace time.Duration
}

func New(ctx context.Context, cfg *config.Config, logger *slog.Logger, version string) (*App, error) {
	keyring, err := tokencrypt.NewKeyring(cfg.EncryptionKeys)
	if err != nil {
		return nil, fmt.Errorf("loading encryption keys: %w", err)
	}
	client := mealie.New(cfg.MealieURL, mealie.Options{UserAgent: userAgentPrefix + version})
	check := startupCheck{
		mealie:       client,
		mealieURL:    cfg.MealieURL.String(),
		logger:       logger,
		retries:      startupRetries,
		retryDelay:   startupRetryDelay,
		reachTimeout: startupReachLimit,
	}
	if err := check.run(ctx); err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, cfg.DataDir, logger)
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}
	a := &App{cfg: cfg, logger: logger, store: st, mealie: client, now: time.Now, shutdownGrace: shutdownGracePeriod}
	a.handler, err = a.routes(keyring, version)
	if err != nil {
		return nil, errors.Join(err, st.Close())
	}
	return a, nil
}

func (a *App) Handler() http.Handler {
	return a.handler
}

func (a *App) Close() error {
	return a.store.Close()
}

func (a *App) routes(keyring *tokencrypt.Keyring, version string) (http.Handler, error) {
	auditLog := audit.New(a.logger)
	oauthServer, err := oauth.New(a.oauthConfig(), oauth.Deps{
		Store:   a.store,
		Keyring: keyring,
		Mealie:  a.mealie,
		CIMD:    oauth.NewCIMDFetcher(oauth.CIMDOptions{}),
		Audit:   auditLog,
		Logger:  a.logger,
		Now:     a.now,
	})
	if err != nil {
		return nil, fmt.Errorf("building oauth server: %w", err)
	}
	sessions := session.New(session.Deps{
		Store:    a.store,
		Keyring:  keyring,
		Mealie:   a.mealie,
		Audit:    auditLog,
		Logger:   a.logger,
		Resource: a.cfg.ResourceURL(),
		Now:      a.now,
	})
	tools := mcpserver.New(mcpserver.Deps{Tokens: sessions, Mealie: a.mealie, Version: version})

	mux := http.NewServeMux()
	oauthServer.Routes(mux)
	mux.Handle("/mcp", a.mcpHandler(sessions, tools))
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /readyz", a.readyz)
	return a.withMiddleware(mux), nil
}

func (a *App) oauthConfig() oauth.Config {
	return oauth.Config{
		Issuer:         a.cfg.PublicURL.String(),
		Resource:       a.cfg.ResourceURL(),
		CallbackURL:    a.cfg.CallbackURL(),
		AccessTokenTTL: a.cfg.AccessTokenTTL,
		SessionMaxAge:  a.cfg.SessionMaxAge,
		TrustedProxies: a.cfg.TrustedProxies,
		SecureCookies:  a.publicURLIsHTTPS(),
	}
}

func (a *App) publicURLIsHTTPS() bool {
	return a.cfg.PublicURL.Scheme == "https"
}

func (a *App) mcpHandler(sessions *session.Manager, tools *mcp.Server) http.Handler {
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return tools }, &mcp.StreamableHTTPOptions{
		SessionTimeout:      mcpSessionTimeout,
		MaxRequestBodyBytes: mcpMaxRequestBytes,
		Logger:              a.logger,
	})
	requireToken := auth.RequireBearerToken(sessions.Verify, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: a.cfg.PublicURL.String() + resourceMetadataURI,
		Scopes:              []string{mcpScope},
	})
	return http.NewCrossOriginProtection().Handler(requireToken(streamable))
}

func (a *App) withMiddleware(next http.Handler) http.Handler {
	next = httpx.SecurityHeaders(a.publicURLIsHTTPS())(next)
	next = obs.AccessLog(a.logger)(next)
	next = obs.RequestID(next)
	return httpx.Recover(a.logger)(next)
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeText(w, http.StatusOK, "ok")
}

func (a *App) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		a.notReady(w, r, "database not ready", err)
		return
	}
	if _, err := a.mealie.About(ctx); err != nil {
		a.notReady(w, r, "mealie not reachable", err)
		return
	}
	writeText(w, http.StatusOK, "ok")
}

func (a *App) notReady(w http.ResponseWriter, r *http.Request, check string, err error) {
	a.logger.WarnContext(r.Context(), "readiness check failed", slog.String("check", check), slog.Any("error", err))
	writeText(w, http.StatusServiceUnavailable, check)
}

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (a *App) Run(ctx context.Context) error {
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", a.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", a.cfg.ListenAddr, err)
	}
	a.logger.InfoContext(ctx, "listening", slog.String("addr", listener.Addr().String()))

	ctx, stop := context.WithCancel(ctx)
	var cleanup sync.WaitGroup
	cleanup.Go(func() { a.cleanupLoop(ctx) })

	requests, endRequests := context.WithCancel(context.WithoutCancel(ctx))
	defer endRequests()
	srv := a.httpServer(requests)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()

	select {
	case err = <-served:
		err = fmt.Errorf("serving http: %w", err)
	case <-ctx.Done():
		err = a.shutdown(ctx, srv, endRequests)
	}
	stop()
	cleanup.Wait()
	return err
}

func (a *App) httpServer(requests context.Context) *http.Server {
	return &http.Server{
		Handler:           a.handler,
		BaseContext:       func(net.Listener) context.Context { return requests },
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(a.logger.Handler(), slog.LevelWarn),
	}
}

func (a *App) shutdown(ctx context.Context, srv *http.Server, endRequests context.CancelFunc) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	graceOver := time.AfterFunc(a.shutdownGrace, endRequests)
	defer graceOver.Stop()
	if err := srv.Shutdown(ctx); err != nil {
		return errors.Join(fmt.Errorf("shutting down http server: %w", err), srv.Close())
	}
	return nil
}

func (a *App) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.deleteExpired(ctx)
		}
	}
}

func (a *App) deleteExpired(ctx context.Context) {
	deleted, err := a.store.DeleteExpired(ctx, a.now())
	if err != nil {
		a.logger.ErrorContext(ctx, "deleting expired rows failed", slog.Any("error", err))
		return
	}
	a.logger.InfoContext(ctx, "deleted expired rows", slog.Int64("deleted", deleted))
}

type incompatibleMealie string

func (e incompatibleMealie) Error() string {
	return string(e)
}

type mealieProbe interface {
	About(ctx context.Context) (mealie.About, error)
	NativeConfig(ctx context.Context) (mealie.NativeConfig, error)
}

type startupCheck struct {
	mealie       mealieProbe
	mealieURL    string
	logger       *slog.Logger
	retries      int
	retryDelay   time.Duration
	reachTimeout time.Duration
}

func (c startupCheck) run(ctx context.Context) error {
	about, err := c.about(ctx)
	if err != nil {
		return err
	}
	if !about.EnableOIDC {
		return incompatibleMealie("Mealie has OIDC disabled; this server requires OIDC (see README)")
	}
	if err := c.checkVersion(ctx, about.Version); err != nil {
		return err
	}
	if _, err := c.mealie.NativeConfig(ctx); err != nil {
		return fmt.Errorf("reading Mealie's native OIDC config: %w", err)
	}
	return nil
}

func (c startupCheck) about(ctx context.Context) (mealie.About, error) {
	ctx, cancel := context.WithTimeout(ctx, c.reachTimeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		about, err := c.mealie.About(ctx)
		if err == nil {
			return about, nil
		}
		if attempt == c.retries || !mealie.IsUnavailable(err) {
			return mealie.About{}, c.unreachable(err)
		}
		c.logger.WarnContext(ctx, "Mealie is not reachable yet, retrying", slog.Int("attempt", attempt+1))
		if waitErr := sleep(ctx, c.retryDelay); waitErr != nil {
			return mealie.About{}, c.unreachable(errors.Join(err, waitErr))
		}
	}
}

func (c startupCheck) unreachable(err error) error {
	return fmt.Errorf("cannot reach Mealie at %s: %w", c.mealieURL, err)
}

func (c startupCheck) checkVersion(ctx context.Context, version string) error {
	order, err := mealie.CompareVersions(version, mealie.MinVersion)
	if err != nil {
		c.logger.WarnContext(ctx, "cannot parse the Mealie version, skipping the version check", slog.String("version", version))
		return nil
	}
	if order < 0 {
		return incompatibleMealie(fmt.Sprintf("Mealie %s is older than %s", version, mealie.MinVersion))
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
