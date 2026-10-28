package tokencrypt

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/config"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
)

func testKey(id string, fill byte) config.EncryptionKey {
	return config.EncryptionKey{ID: id, Material: secret.New(bytes.Repeat([]byte{fill}, 32))}
}

func mustKeyring(t *testing.T, keys ...config.EncryptionKey) *Keyring {
	t.Helper()
	keyring, err := NewKeyring(keys)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return keyring
}

func mustSeal(t *testing.T, keyring *Keyring, plaintext, aad []byte) []byte {
	t.Helper()
	sealed, err := keyring.Seal(plaintext, aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	return sealed
}

func TestSealOpen_RoundTrip(t *testing.T) {
	keyring := mustKeyring(t, testKey("k1", 1))
	plaintext := []byte("mealie-access-token")
	aad := []byte("user-42")

	opened, err := keyring.Open(mustSeal(t, keyring, plaintext, aad), aad)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Errorf("Open = %q, want %q", opened, plaintext)
	}
}

func TestSeal_Layout(t *testing.T) {
	keyring := mustKeyring(t, testKey("k1", 1))
	sealed := mustSeal(t, keyring, []byte("x"), nil)
	if sealed[0] != 0x01 || sealed[1] != 2 || string(sealed[2:4]) != "k1" {
		t.Errorf("unexpected header % x", sealed[:4])
	}
}

func TestNewKeyring_Invalid(t *testing.T) {
	tests := []struct {
		name string
		keys []config.EncryptionKey
	}{
		{"empty", nil},
		{"short material", []config.EncryptionKey{{ID: "k1", Material: secret.New([]byte("short"))}}},
		{"id too long", []config.EncryptionKey{testKey(strings.Repeat("a", 256), 1)}},
		{"duplicate id", []config.EncryptionKey{testKey("k1", 1), testKey("k1", 2)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewKeyring(tt.keys); err == nil {
				t.Error("NewKeyring succeeded, want error")
			}
		})
	}
}

func TestOpen_WrongAAD(t *testing.T) {
	keyring := mustKeyring(t, testKey("k1", 1))
	sealed := mustSeal(t, keyring, []byte("secret"), []byte("user-1"))

	if _, err := keyring.Open(sealed, []byte("user-2")); !errors.Is(err, ErrDecrypt) {
		t.Errorf("Open error = %v, want ErrDecrypt", err)
	}
}

func TestOpen_Tampered(t *testing.T) {
	keyring := mustKeyring(t, testKey("k1", 1))
	aad := []byte("aad")
	sealed := mustSeal(t, keyring, []byte("secret"), aad)

	for i := range sealed {
		tampered := bytes.Clone(sealed)
		tampered[i] ^= 0xff
		_, err := keyring.Open(tampered, aad)
		if !errors.Is(err, ErrDecrypt) && !errors.Is(err, ErrUnknownKey) {
			t.Errorf("byte %d: Open error = %v, want ErrDecrypt or ErrUnknownKey", i, err)
		}
	}
}

func TestRotation(t *testing.T) {
	k1, k2 := testKey("k1", 1), testKey("k2", 2)
	plaintext, aad := []byte("secret"), []byte("aad")

	oldSealed := mustSeal(t, mustKeyring(t, k1), plaintext, aad)
	rotated := mustKeyring(t, k2, k1)
	if opened, err := rotated.Open(oldSealed, aad); err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("rotated keyring Open = %q, %v", opened, err)
	}

	newSealed := mustSeal(t, rotated, plaintext, aad)
	if opened, err := mustKeyring(t, k2).Open(newSealed, aad); err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("k2-only keyring Open = %q, %v", opened, err)
	}
	if _, err := mustKeyring(t, k1).Open(newSealed, aad); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("k1-only keyring Open error = %v, want ErrUnknownKey", err)
	}
}

func TestSeal_NonceUnique(t *testing.T) {
	keyring := mustKeyring(t, testKey("k1", 1))
	seen := make(map[string]struct{})
	for range 1000 {
		seen[string(mustSeal(t, keyring, []byte("same"), nil))] = struct{}{}
	}
	if len(seen) != 1000 {
		t.Errorf("got %d distinct ciphertexts, want 1000", len(seen))
	}
}

func TestOpen_Truncated(t *testing.T) {
	keyring := mustKeyring(t, testKey("k1", 1))
	sealed := mustSeal(t, keyring, []byte("secret"), nil)

	for length := 0; length <= 30; length++ {
		if _, err := keyring.Open(sealed[:min(length, len(sealed))], nil); err == nil {
			t.Errorf("length %d: Open succeeded, want error", length)
		}
	}
}

func TestOpen_UnsupportedVersion(t *testing.T) {
	keyring := mustKeyring(t, testKey("k1", 1))
	sealed := mustSeal(t, keyring, []byte("secret"), nil)
	sealed[0] = 0x02

	if _, err := keyring.Open(sealed, nil); !errors.Is(err, ErrDecrypt) {
		t.Errorf("Open error = %v, want ErrDecrypt", err)
	}
}

func TestErrors_ContainNoKeyMaterial(t *testing.T) {
	keyring := mustKeyring(t, testKey("k1", 1))
	sealed := mustSeal(t, keyring, []byte("secret"), nil)

	_, err := keyring.Open(sealed, []byte("other"))
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("error %v leaks or is nil", err)
	}
}

func FuzzOpen(f *testing.F) {
	keyring, err := NewKeyring([]config.EncryptionKey{testKey("k1", 1)})
	if err != nil {
		f.Fatalf("NewKeyring: %v", err)
	}
	sealed, err := keyring.Seal([]byte("secret"), []byte("aad"))
	if err != nil {
		f.Fatalf("Seal: %v", err)
	}
	f.Add(sealed, []byte("aad"))
	f.Add([]byte{}, []byte{})
	f.Add([]byte{0x01, 0xff}, []byte("aad"))
	f.Fuzz(func(_ *testing.T, ciphertext, aad []byte) {
		_, _ = keyring.Open(ciphertext, aad)
	})
}
