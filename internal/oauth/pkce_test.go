package oauth

import (
	"strings"
	"testing"
)

const (
	rfc7636Verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	rfc7636Challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

func TestS256(t *testing.T) {
	if got := S256(rfc7636Verifier); got != rfc7636Challenge {
		t.Errorf("S256 = %q, want %q", got, rfc7636Challenge)
	}
}

func TestVerifyPKCE(t *testing.T) {
	minimum := strings.Repeat("a", 43)
	maximum := strings.Repeat("a", 128)
	short := strings.Repeat("a", 42)
	long := strings.Repeat("a", 129)
	unreserved := "ABCXYZabcxyz0189-._~" + strings.Repeat("q", 23)
	reserved := strings.Repeat("a", 42) + "+"
	nonASCII := strings.Repeat("a", 42) + "é"

	tests := []struct {
		name      string
		challenge string
		verifier  string
		want      bool
	}{
		{"rfc 7636 appendix b", rfc7636Challenge, rfc7636Verifier, true},
		{"wrong verifier", rfc7636Challenge, minimum, false},
		{"short verifier", S256(short), short, false},
		{"long verifier", S256(long), long, false},
		{"minimum length", S256(minimum), minimum, true},
		{"maximum length", S256(maximum), maximum, true},
		{"all unreserved characters", S256(unreserved), unreserved, true},
		{"reserved character", S256(reserved), reserved, false},
		{"non ascii character", S256(nonASCII), nonASCII, false},
		{"plain challenge", rfc7636Verifier, rfc7636Verifier, false},
		{"empty challenge", "", rfc7636Verifier, false},
		{"empty verifier", S256(""), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VerifyPKCE(tt.challenge, tt.verifier); got != tt.want {
				t.Errorf("VerifyPKCE = %v, want %v", got, tt.want)
			}
		})
	}
}
