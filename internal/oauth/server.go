package oauth

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/audit"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/httpx"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/tokencrypt"
)

const (
	authRequestsPerMinute = 30
	authRequestBurst      = 10
)

//go:embed templates/*.html
var templateFiles embed.FS

type Config struct {
	Issuer         string
	Resource       string
	CallbackURL    string
	AccessTokenTTL time.Duration
	SessionMaxAge  time.Duration
	TrustedProxies []netip.Prefix
	SecureCookies  bool
}

type Store interface {
	UpsertClient(ctx context.Context, c store.Client) error
	GetClient(ctx context.Context, id string) (store.Client, error)
	CreatePendingAuth(ctx context.Context, p store.PendingAuth) error
	GetPendingAuth(ctx context.Context, id string) (store.PendingAuth, error)
	TakePendingAuthByUpstreamState(ctx context.Context, stateHash []byte, now time.Time) (store.PendingAuth, error)
	SetPendingAuthLogin(ctx context.Context, id string, l store.Login) error
	DeletePendingAuth(ctx context.Context, id string) error
	HasConsent(ctx context.Context, userID, clientID string) (bool, error)
	GrantConsent(ctx context.Context, userID, clientID string, now time.Time) error
	CreateSessionWithCode(ctx context.Context, session store.Session, code store.AuthCode) error
}

type MealieAuth interface {
	NativeConfig(ctx context.Context) (mealie.NativeConfig, error)
	NativeToken(ctx context.Context, req mealie.NativeTokenRequest) (mealie.Token, error)
	Self(ctx context.Context, tok secret.Secret) (mealie.User, error)
}

type ClientResolver interface {
	Fetch(ctx context.Context, clientID string) (ClientMetadata, error)
}

type Deps struct {
	Store   Store
	Keyring *tokencrypt.Keyring
	Mealie  MealieAuth
	CIMD    ClientResolver
	Audit   *audit.Logger
	Logger  *slog.Logger
	Now     func() time.Time
}

type Server struct {
	cfg       Config
	store     Store
	keyring   *tokencrypt.Keyring
	mealie    MealieAuth
	cimd      ClientResolver
	audit     *audit.Logger
	logger    *slog.Logger
	now       func() time.Time
	rateLimit func(http.Handler) http.Handler
	consent   *template.Template
	errorPage *template.Template
}

func New(cfg Config, d Deps) (*Server, error) {
	consent, err := template.ParseFS(templateFiles, "templates/consent.html")
	if err != nil {
		return nil, fmt.Errorf("parsing consent template: %w", err)
	}
	errorPage, err := template.ParseFS(templateFiles, "templates/error.html")
	if err != nil {
		return nil, fmt.Errorf("parsing error template: %w", err)
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &Server{
		cfg:       cfg,
		store:     d.Store,
		keyring:   d.Keyring,
		mealie:    d.Mealie,
		cimd:      d.CIMD,
		audit:     d.Audit,
		logger:    d.Logger,
		now:       now,
		rateLimit: httpx.RateLimit(authRequestsPerMinute, authRequestBurst, cfg.TrustedProxies),
		consent:   consent,
		errorPage: errorPage,
	}, nil
}

func (s *Server) Routes(mux *http.ServeMux) {
	resourceMetadata := s.protectedResourceMetadataHandler()
	mux.Handle("GET /.well-known/oauth-authorization-server", s.authorizationServerMetadataHandler())
	mux.Handle("GET /.well-known/oauth-protected-resource", resourceMetadata)
	mux.Handle("GET /.well-known/oauth-protected-resource/mcp", resourceMetadata)
	mux.Handle("POST /register", s.rateLimit(http.HandlerFunc(s.register)))
	mux.Handle("GET /authorize", s.rateLimit(http.HandlerFunc(s.authorize)))
	mux.Handle("GET /callback", s.rateLimit(http.HandlerFunc(s.callback)))
	mux.HandleFunc("POST /consent", s.decideConsent)
}
