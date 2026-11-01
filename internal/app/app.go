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
	"go.opentelemetry.io/otel/trace"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/audit"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/config"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/httpx"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/mcpserver"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/metrics"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/oauth"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/obs"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/session"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/tokencrypt"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/tracing"
)

const (
	readHeaderTimeout   = 10 * time.Second
	readTimeout         = 30 * time.Second
	writeTimeout        = 0
	idleTimeout         = 120 * time.Second
	maxHeaderBytes      = 64 << 10
	shutdownTimeout     = 20 * time.Second
	shutdownGracePeriod = 5 * time.Second
	metricsWriteTimeout = 10 * time.Second
	tracingFlushTimeout = 5 * time.Second

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

type namedServer struct {
	name     string
	http     *http.Server
	listener net.Listener
}

func (s namedServer) serve() error {
	return fmt.Errorf("serving %s: %w", s.name, s.http.Serve(s.listener))
}

type App struct {
	cfg           *config.Config
	logger        *slog.Logger
	store         *store.Store
	mealie        *mealie.Client
	now           func() time.Time
	handler       http.Handler
	shutdownGrace time.Duration
	metrics       *metrics.Registry
	tracing       trace.TracerProvider
	flushTracing  func() error
}

func New(ctx context.Context, cfg *config.Config, logger *slog.Logger, version string) (*App, error) {
	return newApp(ctx, cfg, logger, version, mcpserver.New, metrics.New, tracing.OTLP)
}

func newApp(ctx context.Context, cfg *config.Config, logger *slog.Logger, version string, buildTools func(mcpserver.Deps) (*mcp.Server, error), newMetrics func() (*metrics.Registry, error), export tracing.Exporter) (*App, error) {
	keyring, err := tokencrypt.NewKeyring(cfg.EncryptionKeys)
	if err != nil {
		return nil, fmt.Errorf("loading encryption keys: %w", err)
	}
	registry, err := newMetrics()
	if err != nil {
		return nil, fmt.Errorf("starting metrics: %w", err)
	}
	provider, shutdownTracing, err := tracing.Setup(ctx, cfg.TracingEnabled, version, export, logger)
	if err != nil {
		return nil, fmt.Errorf("starting tracing: %w", err)
	}
	a := &App{
		cfg:           cfg,
		logger:        logger,
		now:           time.Now,
		shutdownGrace: shutdownGracePeriod,
		metrics:       registry,
		tracing:       provider,
		flushTracing:  flushWithin(context.WithoutCancel(ctx), shutdownTracing),
	}
	if err := a.wire(ctx, keyring, version, buildTools); err != nil {
		return nil, errors.Join(err, a.flushTracing())
	}
	return a, nil
}

func (a *App) wire(ctx context.Context, keyring *tokencrypt.Keyring, version string, buildTools func(mcpserver.Deps) (*mcp.Server, error)) error {
	warnWithoutTrustedProxies(ctx, a.cfg, a.logger)
	a.mealie = mealie.New(a.cfg.MealieURL, mealie.Options{UserAgent: userAgentPrefix + version, Metrics: a.metrics, Tracing: a.tracing})
	check := startupCheck{
		mealie:       a.mealie,
		mealieURL:    a.cfg.MealieURL.String(),
		logger:       a.logger,
		retries:      startupRetries,
		retryDelay:   startupRetryDelay,
		reachTimeout: startupReachLimit,
	}
	if err := check.run(ctx); err != nil {
		return err
	}
	st, err := store.Open(ctx, a.cfg.DataDir, a.logger)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	a.store = st
	auditLog := audit.New(a.logger, a.metrics)
	sessions := a.sessionManager(keyring, auditLog)
	tools, err := buildTools(mcpserver.Deps{
		Tokens:  sessions,
		Mealie:  a.mealie,
		Clock:   a.now,
		Version: version,
		Logger:  a.logger,
		Metrics: a.metrics,
		Tracing: a.tracing,
	})
	if err != nil {
		return errors.Join(fmt.Errorf("building MCP tools: %w", err), st.Close())
	}
	a.handler = a.routes(keyring, auditLog, sessions, tools)
	return nil
}

func warnWithoutTrustedProxies(ctx context.Context, cfg *config.Config, logger *slog.Logger) {
	if len(cfg.TrustedProxies) == 0 {
		logger.WarnContext(ctx, "TRUSTED_PROXIES is not set, so rate limiting treats a reverse proxy as a single client; set it to the proxy's CIDR (see README)")
	}
}

func (a *App) Handler() http.Handler {
	return a.handler
}

func (a *App) Close() error {
	return errors.Join(a.flushTracing(), a.store.Close())
}

func flushWithin(ctx context.Context, shutdown func(context.Context) error) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(ctx, tracingFlushTimeout)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			return fmt.Errorf("flushing traces: %w", err)
		}
		return nil
	}
}

func (a *App) sessionManager(keyring *tokencrypt.Keyring, auditLog *audit.Logger) *session.Manager {
	return session.New(session.Deps{
		Store:    a.store,
		Keyring:  keyring,
		Mealie:   a.mealie,
		Audit:    auditLog,
		Logger:   a.logger,
		Resource: a.cfg.ResourceURL(),
		Now:      a.now,
	})
}

func (a *App) routes(keyring *tokencrypt.Keyring, auditLog *audit.Logger, sessions *session.Manager, tools *mcp.Server) http.Handler {
	oauthServer := oauth.New(a.oauthConfig(), oauth.Deps{
		Store:   a.store,
		Keyring: keyring,
		Mealie:  a.mealie,
		CIMD:    oauth.NewCIMDFetcher(oauth.CIMDOptions{}),
		Audit:   auditLog,
		Logger:  a.logger,
		Now:     a.now,
	})

	mux := http.NewServeMux()
	oauthServer.Routes(mux)
	mux.Handle("/mcp", a.mcpHandler(sessions, tools))
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /readyz", a.readyz)
	return a.withMiddleware(mux)
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
	next = httpx.Recover(a.logger)(next)
	next = a.metrics.Middleware(next)
	next = httpx.SecurityHeaders(a.publicURLIsHTTPS())(next)
	next = obs.AccessLog(a.logger)(next)
	next = tracing.Middleware(a.tracing)(next)
	return obs.RequestID(next)
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
	if a.cfg.MetricsAddr == "" {
		return a.serve(ctx, listener, nil)
	}
	metricsListener, err := new(net.ListenConfig).Listen(ctx, "tcp", a.cfg.MetricsAddr)
	if err != nil {
		return errors.Join(fmt.Errorf("listening on METRICS_ADDR %s: %w", a.cfg.MetricsAddr, err), listener.Close())
	}
	a.logger.InfoContext(ctx, "serving metrics", slog.String("addr", metricsListener.Addr().String()))
	return a.serve(ctx, listener, metricsListener)
}

func (a *App) serve(ctx context.Context, listener, metricsListener net.Listener) error {
	ctx, stop := context.WithCancel(ctx)
	a.refreshActiveSessions(ctx)
	var cleanup sync.WaitGroup
	cleanup.Go(func() { a.cleanupLoop(ctx, cleanupInterval) })

	requests, endRequests := context.WithCancel(context.WithoutCancel(ctx))
	defer endRequests()
	servers := []namedServer{{name: "http", http: a.httpServer(requests), listener: listener}}
	if metricsListener != nil {
		servers = append(servers, namedServer{name: "metrics", http: a.metricsServer(), listener: metricsListener})
	}
	served := make(chan error, len(servers))
	var serving sync.WaitGroup
	for _, s := range servers {
		serving.Go(func() { served <- s.serve() })
	}

	var err error
	select {
	case err = <-served:
	case <-ctx.Done():
	}
	err = errors.Join(err, a.shutdown(ctx, servers, endRequests))
	serving.Wait()
	stop()
	cleanup.Wait()
	return err
}

func (a *App) httpServer(requests context.Context) *http.Server {
	srv := a.newServer(a.handler, writeTimeout)
	srv.BaseContext = func(net.Listener) context.Context { return requests }
	return srv
}

func (a *App) metricsServer() *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", a.metrics.Handler())
	return a.newServer(mux, metricsWriteTimeout)
}

func (a *App) newServer(handler http.Handler, writeLimit time.Duration) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeLimit,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(a.logger.Handler(), slog.LevelWarn),
	}
}

func (a *App) shutdown(ctx context.Context, servers []namedServer, endRequests context.CancelFunc) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	graceOver := time.AfterFunc(a.shutdownGrace, endRequests)
	defer graceOver.Stop()
	var errs []error
	for _, s := range servers {
		if err := s.http.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutting down %s server: %w", s.name, err), s.http.Close())
		}
	}
	return errors.Join(errs...)
}

func (a *App) cleanupLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
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
	a.refreshActiveSessions(ctx)
}

func (a *App) refreshActiveSessions(ctx context.Context) {
	count, err := a.store.CountActiveSessions(ctx, a.now())
	if err != nil {
		a.logger.ErrorContext(ctx, "counting active sessions failed", slog.Any("error", err))
		return
	}
	a.metrics.SetActiveSessions(ctx, count)
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
