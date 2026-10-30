package token

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

const unreservedSet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"

func TestNew_FormatAndHash(t *testing.T) {
	tests := []struct {
		name   string
		kind   Kind
		prefix string
	}{
		{"access", Access, "mmcp_at_"},
		{"refresh", Refresh, "mmcp_rt_"},
		{"code", Code, "mmcp_ac_"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plain, hash := New(tt.kind)
			got := plain.Reveal()
			if !strings.HasPrefix(got, tt.prefix) {
				t.Fatalf("missing prefix %q", tt.prefix)
			}
			if len(got) != len(tt.prefix)+43 {
				t.Fatalf("length = %d, want %d", len(got), len(tt.prefix)+43)
			}
			if string(Hash(got)) != string(hash) {
				t.Fatal("hash does not match Hash(plain)")
			}
			sum := sha256.Sum256([]byte(got))
			if string(hash) != string(sum[:]) {
				t.Fatal("hash is not SHA-256 of plaintext")
			}
		})
	}
}

func TestNew_Unique(t *testing.T) {
	a, _ := New(Access)
	b, _ := New(Access)
	if a.Reveal() == b.Reveal() {
		t.Fatal("two tokens are identical")
	}
}

func TestHasKind(t *testing.T) {
	access, _ := New(Access)
	refresh, _ := New(Refresh)
	tests := []struct {
		name  string
		plain string
		kind  Kind
		want  bool
	}{
		{"access is access", access.Reveal(), Access, true},
		{"access is not refresh", access.Reveal(), Refresh, false},
		{"refresh is refresh", refresh.Reveal(), Refresh, true},
		{"refresh is not code", refresh.Reveal(), Code, false},
		{"empty", "", Access, false},
		{"prefix only", string(Access), Access, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasKind(tt.plain, tt.kind); got != tt.want {
				t.Fatalf("HasKind = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRandomVerifier_PKCELength(t *testing.T) {
	v := RandomVerifier()
	if len(v) < 43 || len(v) > 128 {
		t.Fatalf("length = %d", len(v))
	}
	for _, r := range v {
		if !strings.ContainsRune(unreservedSet, r) {
			t.Fatalf("character %q outside unreserved set", r)
		}
	}
	if v == RandomVerifier() {
		t.Fatal("verifiers repeat")
	}
}

func TestRandomID(t *testing.T) {
	id := RandomID()
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) != 16 {
		t.Fatalf("decoded length = %d, want 16", len(raw))
	}
	if id == RandomID() {
		t.Fatal("ids repeat")
	}
}
