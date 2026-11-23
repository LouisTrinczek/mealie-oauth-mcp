package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net/netip"
	"strings"
	"testing"
	"time"
)

const urlPassword = "s3cretvalue"

func keyMaterial(size int) string {
	raw := make([]byte, size)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func validEnv() map[string]string {
	return map[string]string{
		"MEALIE_URL":      "https://mealie.example",
		"PUBLIC_URL":      "https://mcp.example",
		"ENCRYPTION_KEYS": "k1:" + keyMaterial(32),
	}
}

func ptr(value string) *string {
	return &value
}

func loadFrom(env map[string]string, files map[string][]byte) (*Config, error) {
	getenv := func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	}
	readFile := func(path string) ([]byte, error) {
		content, ok := files[path]
		if !ok {
			return nil, errors.New("no such file")
		}
		return content, nil
	}
	return Load(getenv, readFile)
}

func withEnv(overrides map[string]string) map[string]string {
	env := validEnv()
	for name, value := range overrides {
		env[name] = value
	}
	return env
}

func TestLoad_MinimalValid(t *testing.T) {
	cfg, err := loadFrom(validEnv(), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	checks := []struct {
		name string
		ok   bool
	}{
		{"MealieURL", cfg.MealieURL.String() == "https://mealie.example"},
		{"PublicURL", cfg.PublicURL.String() == "https://mcp.example"},
		{"DataDir", cfg.DataDir == "/data"},
		{"ListenAddr", cfg.ListenAddr == ":8080"},
		{"SessionMaxAge", cfg.SessionMaxAge == 720*time.Hour},
		{"AccessTokenTTL", cfg.AccessTokenTTL == time.Hour},
		{"LogLevel", cfg.LogLevel == slog.LevelInfo},
		{"LogFormat", cfg.LogFormat == "json"},
		{"TrustedProxies", len(cfg.TrustedProxies) == 0},
		{"InsecureAllowHTTP", !cfg.InsecureAllowHTTP},
		{"ResourceURL", cfg.ResourceURL() == "https://mcp.example/mcp"},
		{"CallbackURL", cfg.CallbackURL() == "https://mcp.example/callback"},
		{"EncryptionKeys", len(cfg.EncryptionKeys) == 1 &&
			cfg.EncryptionKeys[0].ID == "k1" &&
			len(cfg.EncryptionKeys[0].Material.Bytes()) == 32},
	}
	for _, check := range checks {
		if !check.ok {
			t.Errorf("unexpected value for %s: %+v", check.name, cfg)
		}
	}
}

func TestLoad_AllOverrides(t *testing.T) {
	env := withEnv(map[string]string{
		"ENCRYPTION_KEYS":     "new:" + keyMaterial(32) + ",old-1:" + keyMaterial(32),
		"DATA_DIR":            "/var/lib/mcp",
		"LISTEN_ADDR":         "127.0.0.1:9000",
		"SESSION_MAX_AGE":     "48h",
		"ACCESS_TOKEN_TTL":    "30m",
		"LOG_LEVEL":           "debug",
		"LOG_FORMAT":          "text",
		"TRUSTED_PROXIES":     "10.0.0.0/8, 192.168.1.0/24",
		"INSECURE_ALLOW_HTTP": "true",
		"PUBLIC_URL":          "http://localhost:8080",
	})
	cfg, err := loadFrom(env, nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.EncryptionKeys) != 2 || cfg.EncryptionKeys[0].ID != "new" || cfg.EncryptionKeys[1].ID != "old-1" {
		t.Errorf("EncryptionKeys = %+v", cfg.EncryptionKeys)
	}
	wantProxies := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.1.0/24")}
	if len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[0] != wantProxies[0] || cfg.TrustedProxies[1] != wantProxies[1] {
		t.Errorf("TrustedProxies = %v", cfg.TrustedProxies)
	}
	if cfg.DataDir != "/var/lib/mcp" || cfg.ListenAddr != "127.0.0.1:9000" {
		t.Errorf("DataDir/ListenAddr = %q %q", cfg.DataDir, cfg.ListenAddr)
	}
	if cfg.SessionMaxAge != 48*time.Hour || cfg.AccessTokenTTL != 30*time.Minute {
		t.Errorf("durations = %v %v", cfg.SessionMaxAge, cfg.AccessTokenTTL)
	}
	if cfg.LogLevel != slog.LevelDebug || cfg.LogFormat != "text" || !cfg.InsecureAllowHTTP {
		t.Errorf("log/insecure = %v %q %v", cfg.LogLevel, cfg.LogFormat, cfg.InsecureAllowHTTP)
	}
	if cfg.PublicURL.Scheme != "http" {
		t.Errorf("PublicURL scheme = %q", cfg.PublicURL.Scheme)
	}
}

func TestLoad_LogLevels(t *testing.T) {
	tests := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, err := loadFrom(withEnv(map[string]string{"LOG_LEVEL": name}), nil)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.LogLevel != want {
				t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, want)
			}
		})
	}
}

func TestLoad_FileVariant(t *testing.T) {
	env := validEnv()
	delete(env, "ENCRYPTION_KEYS")
	env["ENCRYPTION_KEYS_FILE"] = "/run/secrets/k"
	files := map[string][]byte{"/run/secrets/k": []byte("k1:" + keyMaterial(32) + "\n")}

	cfg, err := loadFrom(env, files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.EncryptionKeys) != 1 || cfg.EncryptionKeys[0].ID != "k1" {
		t.Errorf("EncryptionKeys = %+v", cfg.EncryptionKeys)
	}
}

func TestLoad_FileVariantUnreadable(t *testing.T) {
	env := validEnv()
	delete(env, "ENCRYPTION_KEYS")
	env["ENCRYPTION_KEYS_FILE"] = "/missing"

	_, err := loadFrom(env, nil)
	if err == nil || !strings.Contains(err.Error(), "ENCRYPTION_KEYS_FILE") {
		t.Errorf("error = %v, want mention of ENCRYPTION_KEYS_FILE", err)
	}
}

func TestLoad_EmptyKeyVariablesTreatedAsUnset(t *testing.T) {
	files := map[string][]byte{"/run/secrets/k": []byte("k1:" + keyMaterial(32))}
	tests := map[string]map[string]string{
		"empty inline with file": {"ENCRYPTION_KEYS": "", "ENCRYPTION_KEYS_FILE": "/run/secrets/k"},
		"empty file with inline": {"ENCRYPTION_KEYS_FILE": ""},
	}
	for name, overrides := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, err := loadFrom(withEnv(overrides), files)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if len(cfg.EncryptionKeys) != 1 || cfg.EncryptionKeys[0].ID != "k1" {
				t.Errorf("EncryptionKeys = %+v", cfg.EncryptionKeys)
			}
		})
	}
}

func TestLoad_AcceptedBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]string
		check     func(*Config) bool
	}{
		{"access ttl 5m", map[string]string{"ACCESS_TOKEN_TTL": "5m"}, func(c *Config) bool { return c.AccessTokenTTL == 5*time.Minute }},
		{"access ttl 24h", map[string]string{"ACCESS_TOKEN_TTL": "24h"}, func(c *Config) bool { return c.AccessTokenTTL == 24*time.Hour }},
		{"session 2160h", map[string]string{"SESSION_MAX_AGE": "2160h"}, func(c *Config) bool { return c.SessionMaxAge == 2160*time.Hour }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadFrom(withEnv(tt.overrides), nil)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !tt.check(cfg) {
				t.Errorf("unexpected config %+v", cfg)
			}
		})
	}
}

func TestLoad_BothKeySourcesRejected(t *testing.T) {
	env := validEnv()
	env["ENCRYPTION_KEYS_FILE"] = "/run/secrets/k"
	files := map[string][]byte{"/run/secrets/k": []byte("k1:" + keyMaterial(32))}

	_, err := loadFrom(env, files)
	if err == nil {
		t.Fatal("Load() error = nil")
	}
	for _, name := range []string{"ENCRYPTION_KEYS", "ENCRYPTION_KEYS_FILE"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

func TestLoad_Errors(t *testing.T) {
	shortKey := keyMaterial(16)
	goodKey := keyMaterial(32)
	tests := []struct {
		name        string
		overrides   map[string]string
		remove      string
		wantVar     string
		forbiddenIn []string
	}{
		{name: "mealie url missing", remove: "MEALIE_URL", wantVar: "MEALIE_URL"},
		{name: "public url missing", remove: "PUBLIC_URL", wantVar: "PUBLIC_URL"},
		{name: "mealie url malformed", overrides: map[string]string{"MEALIE_URL": "not a url"}, wantVar: "MEALIE_URL"},
		{name: "mealie url wrong scheme", overrides: map[string]string{"MEALIE_URL": "ftp://mealie.example"}, wantVar: "MEALIE_URL"},
		{name: "public url with credentials", overrides: map[string]string{"PUBLIC_URL": "https://user:" + urlPassword + "@mcp.example"}, wantVar: "PUBLIC_URL", forbiddenIn: []string{urlPassword}},
		{name: "mealie url with credentials", overrides: map[string]string{"MEALIE_URL": "https://user:" + urlPassword + "@mealie.example"}, wantVar: "MEALIE_URL", forbiddenIn: []string{urlPassword}},
		{name: "mealie url with query", overrides: map[string]string{"MEALIE_URL": "https://mealie.example?a=b"}, wantVar: "MEALIE_URL"},
		{name: "mealie url with fragment", overrides: map[string]string{"MEALIE_URL": "https://mealie.example#frag"}, wantVar: "MEALIE_URL"},
		{name: "insecure flag false with http public url", overrides: map[string]string{"PUBLIC_URL": "http://mcp.example", "INSECURE_ALLOW_HTTP": "false"}, wantVar: "PUBLIC_URL"},
		{name: "public url with path", overrides: map[string]string{"PUBLIC_URL": "https://mcp.example/base"}, wantVar: "PUBLIC_URL"},
		{name: "public url with query", overrides: map[string]string{"PUBLIC_URL": "https://mcp.example?a=b"}, wantVar: "PUBLIC_URL"},
		{name: "public url with fragment", overrides: map[string]string{"PUBLIC_URL": "https://mcp.example#frag"}, wantVar: "PUBLIC_URL"},
		{name: "public url http without opt in", overrides: map[string]string{"PUBLIC_URL": "http://mcp.example"}, wantVar: "PUBLIC_URL"},
		{name: "insecure flag not a bool", overrides: map[string]string{"INSECURE_ALLOW_HTTP": "maybe"}, wantVar: "INSECURE_ALLOW_HTTP"},
		{name: "key not 32 bytes", overrides: map[string]string{"ENCRYPTION_KEYS": "k1:" + shortKey}, wantVar: "ENCRYPTION_KEYS", forbiddenIn: []string{shortKey}},
		{name: "key not base64", overrides: map[string]string{"ENCRYPTION_KEYS": "k1:!!!notbase64!!!"}, wantVar: "ENCRYPTION_KEYS", forbiddenIn: []string{"!!!notbase64!!!"}},
		{name: "key without separator", overrides: map[string]string{"ENCRYPTION_KEYS": goodKey}, wantVar: "ENCRYPTION_KEYS", forbiddenIn: []string{goodKey}},
		{name: "duplicate key ids", overrides: map[string]string{"ENCRYPTION_KEYS": "k1:" + goodKey + ",k1:" + goodKey}, wantVar: "ENCRYPTION_KEYS", forbiddenIn: []string{goodKey}},
		{name: "key id invalid characters", overrides: map[string]string{"ENCRYPTION_KEYS": "bad id:" + goodKey}, wantVar: "ENCRYPTION_KEYS", forbiddenIn: []string{goodKey}},
		{name: "key id too long", overrides: map[string]string{"ENCRYPTION_KEYS": strings.Repeat("a", 33) + ":" + goodKey}, wantVar: "ENCRYPTION_KEYS", forbiddenIn: []string{goodKey}},
		{name: "key id empty", overrides: map[string]string{"ENCRYPTION_KEYS": ":" + goodKey}, wantVar: "ENCRYPTION_KEYS", forbiddenIn: []string{goodKey}},
		{name: "no keys", overrides: map[string]string{"ENCRYPTION_KEYS": ""}, wantVar: "ENCRYPTION_KEYS"},
		{name: "session max age zero", overrides: map[string]string{"SESSION_MAX_AGE": "0"}, wantVar: "SESSION_MAX_AGE"},
		{name: "session max age negative", overrides: map[string]string{"SESSION_MAX_AGE": "-1h"}, wantVar: "SESSION_MAX_AGE"},
		{name: "session max age too large", overrides: map[string]string{"SESSION_MAX_AGE": "2161h"}, wantVar: "SESSION_MAX_AGE"},
		{name: "session max age unparsable", overrides: map[string]string{"SESSION_MAX_AGE": "soon"}, wantVar: "SESSION_MAX_AGE"},
		{name: "access token ttl too small", overrides: map[string]string{"ACCESS_TOKEN_TTL": "4m"}, wantVar: "ACCESS_TOKEN_TTL"},
		{name: "access token ttl too large", overrides: map[string]string{"ACCESS_TOKEN_TTL": "25h"}, wantVar: "ACCESS_TOKEN_TTL"},
		{name: "unknown log level", overrides: map[string]string{"LOG_LEVEL": "verbose"}, wantVar: "LOG_LEVEL"},
		{name: "unknown log format", overrides: map[string]string{"LOG_FORMAT": "xml"}, wantVar: "LOG_FORMAT"},
		{name: "invalid trusted proxy", overrides: map[string]string{"TRUSTED_PROXIES": "10.0.0.0/8,nope"}, wantVar: "TRUSTED_PROXIES"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := withEnv(tt.overrides)
			if tt.remove != "" {
				delete(env, tt.remove)
			}
			cfg, err := loadFrom(env, nil)
			if err == nil {
				t.Fatalf("Load() = %+v, want error", cfg)
			}
			if !strings.Contains(err.Error(), tt.wantVar) {
				t.Errorf("error %q does not name %s", err, tt.wantVar)
			}
			for _, forbidden := range tt.forbiddenIn {
				if strings.Contains(err.Error(), forbidden) {
					t.Errorf("error %q leaks key material", err)
				}
			}
		})
	}
}

func TestLoad_InsecureAllowHTTPPermitsHTTPPublicURL(t *testing.T) {
	env := withEnv(map[string]string{"PUBLIC_URL": "http://mcp.example", "INSECURE_ALLOW_HTTP": "true"})
	if _, err := loadFrom(env, nil); err != nil {
		t.Errorf("Load() error = %v", err)
	}
}

func TestLoad_MultipleErrorsReported(t *testing.T) {
	env := withEnv(map[string]string{"LOG_LEVEL": "verbose", "SESSION_MAX_AGE": "0"})
	_, err := loadFrom(env, nil)
	if err == nil {
		t.Fatal("Load() error = nil")
	}
	for _, name := range []string{"LOG_LEVEL", "SESSION_MAX_AGE"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

func TestLoad_TrailingSlashNormalized(t *testing.T) {
	cfg, err := loadFrom(withEnv(map[string]string{"PUBLIC_URL": "https://mcp.example/"}), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PublicURL.String() != "https://mcp.example" {
		t.Errorf("PublicURL = %q", cfg.PublicURL)
	}
	if cfg.ResourceURL() != "https://mcp.example/mcp" || cfg.CallbackURL() != "https://mcp.example/callback" {
		t.Errorf("derived URLs = %q %q", cfg.ResourceURL(), cfg.CallbackURL())
	}
}

func TestLoad_MetricsAddr(t *testing.T) {
	tests := []struct {
		name  string
		unset bool
		value string
		want  string
	}{
		{name: "unset uses default", unset: true, want: "127.0.0.1:9090"},
		{name: "empty disables", value: "", want: ""},
		{name: "port only host", value: ":9090", want: ":9090"},
		{name: "host and port", value: "0.0.0.0:2112", want: "0.0.0.0:2112"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			if !tt.unset {
				env["METRICS_ADDR"] = tt.value
			}
			cfg, err := loadFrom(env, nil)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.MetricsAddr != tt.want {
				t.Errorf("MetricsAddr = %q, want %q", cfg.MetricsAddr, tt.want)
			}
		})
	}
}

func TestLoad_TracingEnabled(t *testing.T) {
	tests := []struct {
		name     string
		endpoint *string
		want     bool
	}{
		{name: "unset", want: false},
		{name: "empty", endpoint: ptr(""), want: false},
		{name: "http endpoint", endpoint: ptr("http://otel:4318"), want: true},
		{name: "https endpoint", endpoint: ptr("https://otel.example:4318/v1/traces"), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			if tt.endpoint != nil {
				env["OTEL_EXPORTER_OTLP_ENDPOINT"] = *tt.endpoint
			}
			cfg, err := loadFrom(env, nil)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.TracingEnabled != tt.want {
				t.Errorf("TracingEnabled = %v, want %v", cfg.TracingEnabled, tt.want)
			}
		})
	}
}

func TestLoad_ObservabilityErrors(t *testing.T) {
	tests := []struct {
		name      string
		variable  string
		value     string
		wantError string
		secret    string
	}{
		{name: "metrics addr without host or colon", variable: "METRICS_ADDR", value: "9090", wantError: "METRICS_ADDR must be host:port (for example 127.0.0.1:9090), or empty to disable metrics"},
		{name: "metrics addr without port", variable: "METRICS_ADDR", value: "127.0.0.1:", wantError: "METRICS_ADDR must be host:port (for example 127.0.0.1:9090), or empty to disable metrics"},
		{name: "metrics addr non-numeric port", variable: "METRICS_ADDR", value: "127.0.0.1:abc", wantError: "METRICS_ADDR must be host:port (for example 127.0.0.1:9090), or empty to disable metrics"},
		{name: "metrics addr port out of range", variable: "METRICS_ADDR", value: "127.0.0.1:99999", wantError: "METRICS_ADDR must be host:port (for example 127.0.0.1:9090), or empty to disable metrics"},
		{name: "otlp endpoint without scheme", variable: "OTEL_EXPORTER_OTLP_ENDPOINT", value: "otel:4318", wantError: "OTEL_EXPORTER_OTLP_ENDPOINT must be an absolute http or https URL"},
		{name: "otlp endpoint wrong scheme", variable: "OTEL_EXPORTER_OTLP_ENDPOINT", value: "ftp://user:" + urlPassword + "@otel", wantError: "OTEL_EXPORTER_OTLP_ENDPOINT must be an absolute http or https URL", secret: urlPassword},
		{name: "otlp endpoint unparsable", variable: "OTEL_EXPORTER_OTLP_ENDPOINT", value: "http://user:" + urlPassword + "@otel:bad", wantError: "OTEL_EXPORTER_OTLP_ENDPOINT must be an absolute http or https URL", secret: urlPassword},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadFrom(withEnv(map[string]string{tt.variable: tt.value}), nil)
			if err == nil {
				t.Fatalf("Load() = %+v, want error", cfg)
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Errorf("error %q does not contain %q", err, tt.wantError)
			}
			if tt.secret != "" && strings.Contains(err.Error(), tt.secret) {
				t.Errorf("error %q leaks %q", err, tt.secret)
			}
		})
	}
}

func TestDataDir(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"unset", nil, "/data"},
		{"empty", map[string]string{"DATA_DIR": ""}, "/data"},
		{"set", map[string]string{"DATA_DIR": "/srv"}, "/srv"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(name string) (string, bool) {
				value, ok := tt.env[name]
				return value, ok
			}
			if got := DataDir(getenv); got != tt.want {
				t.Errorf("DataDir = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoad_PasswordLoginTransport(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]string
		files     map[string][]byte
		want      string
	}{
		{name: "invalid opt-in", overrides: map[string]string{"ENABLE_PASSWORD_LOGIN": "yes"}, want: "ENABLE_PASSWORD_LOGIN must be a boolean"},
		{name: "missing TLS pair", overrides: map[string]string{"ENABLE_PASSWORD_LOGIN": "true"}, want: "TLS_CERT_FILE and TLS_KEY_FILE"},
		{name: "partial TLS pair", overrides: map[string]string{"TLS_CERT_FILE": "cert.pem"}, want: "TLS_CERT_FILE and TLS_KEY_FILE"},
		{name: "missing TLS certificate", overrides: map[string]string{"TLS_CERT_FILE": "cert.pem", "TLS_KEY_FILE": "key.pem"}, want: "TLS_CERT_FILE could not be read"},
		{name: "missing TLS key", overrides: map[string]string{"TLS_CERT_FILE": "cert.pem", "TLS_KEY_FILE": "key.pem"}, files: map[string][]byte{"cert.pem": []byte("invalid")}, want: "TLS_KEY_FILE could not be read"},
		{name: "invalid TLS pair", overrides: map[string]string{"TLS_CERT_FILE": "cert.pem", "TLS_KEY_FILE": "key.pem"}, files: map[string][]byte{"cert.pem": []byte("invalid"), "key.pem": []byte("invalid")}, want: "loading TLS certificate and key"},
		{name: "plaintext public URL", overrides: map[string]string{"ENABLE_PASSWORD_LOGIN": "true", "INSECURE_ALLOW_HTTP": "true", "PUBLIC_URL": "http://mcp.example"}, want: "PUBLIC_URL must use https"},
		{name: "plaintext Mealie URL", overrides: map[string]string{"ENABLE_PASSWORD_LOGIN": "true", "INSECURE_ALLOW_HTTP": "true", "MEALIE_URL": "http://mealie.example"}, want: "MEALIE_URL must use https"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadFrom(withEnv(tt.overrides), tt.files)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestHealthcheckTLSName(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		want      string
		wantError bool
	}{
		{name: "plaintext listener", env: map[string]string{"PUBLIC_URL": "https://public.example"}},
		{name: "public hostname", env: map[string]string{"TLS_CERT_FILE": "cert.pem", "TLS_KEY_FILE": "key.pem", "PUBLIC_URL": "https://public.example:8443"}, want: "public.example"},
		{name: "name override", env: map[string]string{"TLS_CERT_FILE": "cert.pem", "TLS_KEY_FILE": "key.pem", "HEALTHCHECK_TLS_SERVER_NAME": "listener.example"}, want: "listener.example"},
		{name: "public URL missing", env: map[string]string{"TLS_CERT_FILE": "cert.pem", "TLS_KEY_FILE": "key.pem"}, wantError: true},
		{name: "public hostname missing", env: map[string]string{"TLS_CERT_FILE": "cert.pem", "TLS_KEY_FILE": "key.pem", "PUBLIC_URL": "https://:8443"}, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := HealthcheckTLSName(func(name string) (string, bool) {
				value, ok := tt.env[name]
				return value, ok
			})
			if got != tt.want || (err != nil) != tt.wantError {
				t.Fatalf("HealthcheckTLSName() = %q, %v, want %q and error = %v", got, err, tt.want, tt.wantError)
			}
		})
	}
}

func TestLoad_PasswordLoginCertificateValidity(t *testing.T) {
	tests := []struct {
		name      string
		notBefore time.Duration
		notAfter  time.Duration
		optIn     string
		wantError bool
	}{
		{"valid", -time.Hour, time.Hour, "true", false},
		{"expired", -2 * time.Hour, -time.Hour, "true", true},
		{"not yet valid", time.Hour, 2 * time.Hour, "true", true},
		{"expired with password disabled", -2 * time.Hour, -time.Hour, "false", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			files := listenerTLSFiles(t, now.Add(tt.notBefore), now.Add(tt.notAfter))
			cfg, err := loadFrom(withEnv(map[string]string{
				"ENABLE_PASSWORD_LOGIN": tt.optIn,
				"TLS_CERT_FILE":         "cert.pem",
				"TLS_KEY_FILE":          "key.pem",
			}), files)
			if tt.wantError {
				if err == nil || !strings.Contains(err.Error(), "TLS_CERT_FILE certificate is outside its validity period") {
					t.Fatalf("Load() error = %v, want certificate validity error", err)
				}
				return
			}
			if err != nil || cfg.TLSCertificate == nil || cfg.EnablePasswordLogin != (tt.optIn == "true") {
				t.Fatalf("Load() did not accept the expected native TLS configuration: %v", err)
			}
		})
	}
}

func listenerTLSFiles(t *testing.T, notBefore, notAfter time.Time) map[string][]byte {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: notBefore, NotAfter: notAfter}
	cert, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{
		"cert.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}),
		"key.pem":  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}),
	}
}
