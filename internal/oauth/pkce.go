package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

const (
	minVerifierLength = 43
	maxVerifierLength = 128
)

func S256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func VerifyPKCE(challenge, verifier string) bool {
	if !validVerifier(verifier) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(S256(verifier)), []byte(challenge)) == 1
}

func validVerifier(verifier string) bool {
	if len(verifier) < minVerifierLength || len(verifier) > maxVerifierLength {
		return false
	}
	for i := range len(verifier) {
		if !isUnreserved(verifier[i]) {
			return false
		}
	}
	return true
}

func isUnreserved(c byte) bool {
	switch {
	case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9':
		return true
	default:
		return c == '-' || c == '.' || c == '_' || c == '~'
	}
}
