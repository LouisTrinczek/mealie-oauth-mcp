package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
)

const (
	encryptionKeySize = 32

	defaultDataDir        = "/data"
	defaultListenAddr     = ":8080"
	defaultSessionMaxAge  = 720 * time.Hour
	defaultAccessTokenTTL = time.Hour
	defaultLogLevel       = "info"
	defaultLogFormat      = "json"
	defaultMetricsAddr    = "127.0.0.1:9090"

	maxSessionMaxAge  = 2160 * time.Hour
	minAccessTokenTTL = 5 * time.Minute
	maxAccessTokenTTL = 24 * time.Hour
)

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

type EncryptionKey struct {
	ID       string
	Material secret.Secret
}

type Config struct {
	MealieURL         *url.URL
	PublicURL         *url.URL
	EncryptionKeys    []EncryptionKey
	DataDir           string
	ListenAddr        string
	SessionMaxAge     time.Duration
	AccessTokenTTL    time.Duration
	LogLevel          slog.Level
	LogFormat         string
	TrustedProxies    []netip.Prefix
	InsecureAllowHTTP bool
	MetricsAddr       string
	TracingEnabled    bool
}

func (c *Config) ResourceURL() string {
	return c.PublicURL.String() + "/mcp"
}

func (c *Config) CallbackURL() string {
	return c.PublicURL.String() + "/callback"
}

type environment struct {
	getenv   func(string) (string, bool)
	readFile func(string) ([]byte, error)
}

func (e environment) lookup(name, fallback string) string {
	if value, ok := e.getenv(name); ok && value != "" {
		return value
	}
	return fallback
}

func Load(getenv func(string) (string, bool), readFile func(string) ([]byte, error)) (*Config, error) {
	env := environment{getenv: getenv, readFile: readFile}
	cfg := &Config{
		DataDir:    env.lookup("DATA_DIR", defaultDataDir),
		ListenAddr: env.lookup("LISTEN_ADDR", defaultListenAddr),
	}
	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	var err error
	cfg.InsecureAllowHTTP, err = env.boolean("INSECURE_ALLOW_HTTP")
	collect(err)
	cfg.MealieURL, err = env.baseURL("MEALIE_URL")
	collect(err)
	cfg.PublicURL, err = env.publicURL(cfg.InsecureAllowHTTP)
	collect(err)
	cfg.EncryptionKeys, err = env.encryptionKeys()
	collect(err)
	cfg.SessionMaxAge, err = env.duration("SESSION_MAX_AGE", defaultSessionMaxAge, time.Nanosecond, maxSessionMaxAge, "positive and at most 2160h")
	collect(err)
	cfg.AccessTokenTTL, err = env.duration("ACCESS_TOKEN_TTL", defaultAccessTokenTTL, minAccessTokenTTL, maxAccessTokenTTL, "between 5m and 24h")
	collect(err)
	cfg.LogLevel, err = env.logLevel()
	collect(err)
	cfg.LogFormat, err = env.logFormat()
	collect(err)
	cfg.TrustedProxies, err = env.trustedProxies()
	collect(err)
	cfg.MetricsAddr, err = env.metricsAddr()
	collect(err)
	cfg.TracingEnabled, err = env.tracingEnabled()
	collect(err)

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (e environment) boolean(name string) (bool, error) {
	raw := e.lookup(name, "")
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return value, nil
}

func (e environment) baseURL(name string) (*url.URL, error) {
	raw := e.lookup(name, "")
	if raw == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("%s must be an absolute http or https URL", name)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, fmt.Errorf("%s must not contain credentials, a query or a fragment", name)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed, nil
}

func (e environment) publicURL(insecureAllowHTTP bool) (*url.URL, error) {
	const name = "PUBLIC_URL"
	parsed, err := e.baseURL(name)
	if err != nil {
		return nil, err
	}
	if parsed.Path != "" {
		return nil, fmt.Errorf("%s must not contain a path", name)
	}
	if parsed.Scheme == "http" && !insecureAllowHTTP {
		return nil, fmt.Errorf("%s must use https unless INSECURE_ALLOW_HTTP is set", name)
	}
	return parsed, nil
}

func (e environment) duration(name string, fallback, minimum, maximum time.Duration, bounds string) (time.Duration, error) {
	raw := e.lookup(name, "")
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration such as 1h", name)
	}
	if value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be %s", name, bounds)
	}
	return value, nil
}

func (e environment) logLevel() (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(e.lookup("LOG_LEVEL", defaultLogLevel))); err != nil {
		return 0, errors.New("LOG_LEVEL must be one of debug, info, warn, error")
	}
	return level, nil
}

func (e environment) logFormat() (string, error) {
	format := e.lookup("LOG_FORMAT", defaultLogFormat)
	if format != "json" && format != "text" {
		return "", errors.New("LOG_FORMAT must be json or text")
	}
	return format, nil
}

func (e environment) trustedProxies() ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, entry := range strings.Split(e.lookup("TRUSTED_PROXIES", ""), ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXIES entry %q is not a valid CIDR", entry)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

func (e environment) metricsAddr() (string, error) {
	addr, ok := e.getenv("METRICS_ADDR")
	if !ok {
		return defaultMetricsAddr, nil
	}
	if addr == "" {
		return "", nil
	}
	_, port, err := net.SplitHostPort(addr)
	number, convErr := strconv.Atoi(port)
	if err != nil || convErr != nil || number < 1 || number > 65535 {
		return "", errors.New("METRICS_ADDR must be host:port (for example 127.0.0.1:9090), or empty to disable metrics")
	}
	return addr, nil
}

func (e environment) tracingEnabled() (bool, error) {
	const name = "OTEL_EXPORTER_OTLP_ENDPOINT"
	raw := e.lookup(name, "")
	if raw == "" {
		return false, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false, fmt.Errorf("%s must be an absolute http or https URL", name)
	}
	return true, nil
}

func (e environment) encryptionKeysValue() (string, error) {
	const name, fileName = "ENCRYPTION_KEYS", "ENCRYPTION_KEYS_FILE"
	inline := e.lookup(name, "")
	path := e.lookup(fileName, "")
	switch {
	case inline != "" && path != "":
		return "", fmt.Errorf("%s and %s are mutually exclusive", name, fileName)
	case path != "":
		content, err := e.readFile(path)
		if err != nil {
			return "", fmt.Errorf("%s could not be read", fileName)
		}
		return strings.TrimSpace(string(content)), nil
	}
	return inline, nil
}

func (e environment) encryptionKeys() ([]EncryptionKey, error) {
	value, err := e.encryptionKeysValue()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(value) == "" {
		return nil, errors.New("ENCRYPTION_KEYS is required (or ENCRYPTION_KEYS_FILE)")
	}
	var keys []EncryptionKey
	seen := map[string]bool{}
	for position, entry := range strings.Split(value, ",") {
		key, err := parseEncryptionKey(position+1, strings.TrimSpace(entry))
		if err != nil {
			return nil, err
		}
		if seen[key.ID] {
			return nil, fmt.Errorf("ENCRYPTION_KEYS contains duplicate key ID %q", key.ID)
		}
		seen[key.ID] = true
		keys = append(keys, key)
	}
	return keys, nil
}

func parseEncryptionKey(position int, entry string) (EncryptionKey, error) {
	id, encoded, found := strings.Cut(entry, ":")
	if !found {
		return EncryptionKey{}, fmt.Errorf("ENCRYPTION_KEYS entry %d must be id:base64key", position)
	}
	if !keyIDPattern.MatchString(id) {
		return EncryptionKey{}, fmt.Errorf("ENCRYPTION_KEYS entry %d has an invalid key ID", position)
	}
	material, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return EncryptionKey{}, fmt.Errorf("ENCRYPTION_KEYS key %q is not valid base64", id)
	}
	if len(material) != encryptionKeySize {
		return EncryptionKey{}, fmt.Errorf("ENCRYPTION_KEYS key %q must decode to %d bytes", id, encryptionKeySize)
	}
	return EncryptionKey{ID: id, Material: secret.New(material)}, nil
}
