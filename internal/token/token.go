package token

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
)

type Kind string

const (
	Access  Kind = "mmcp_at_"
	Refresh Kind = "mmcp_rt_"
	Code    Kind = "mmcp_ac_"
)

const (
	tokenBytes    = 32
	idBytes       = 16
	verifierBytes = 32
)

func New(k Kind) (secret.Secret, []byte, error) {
	plain := string(k) + randomString(tokenBytes)
	return secret.FromString(plain), Hash(plain), nil
}

func Hash(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}

func HasKind(plain string, k Kind) bool {
	return len(plain) > len(k) && strings.HasPrefix(plain, string(k))
}

func RandomID() string {
	return randomString(idBytes)
}

func RandomVerifier() string {
	return randomString(verifierBytes)
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
