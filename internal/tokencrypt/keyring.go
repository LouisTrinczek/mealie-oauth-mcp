package tokencrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/config"
)

const (
	formatVersion = 0x01
	keyLength     = 32
	maxKeyIDLen   = 255
)

var (
	ErrUnknownKey = errors.New("ciphertext was sealed with an unknown key")
	ErrDecrypt    = errors.New("ciphertext cannot be decrypted")
)

type Keyring struct {
	encryptingHeader []byte
	encryptingAEAD   cipher.AEAD
	aeads            map[string]cipher.AEAD
}

func NewKeyring(keys []config.EncryptionKey) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, errors.New("keyring needs at least one key")
	}
	aeads := make(map[string]cipher.AEAD, len(keys))
	for _, key := range keys {
		if _, duplicate := aeads[key.ID]; duplicate {
			return nil, fmt.Errorf("duplicate key id %q", key.ID)
		}
		aead, err := newAEAD(key)
		if err != nil {
			return nil, err
		}
		aeads[key.ID] = aead
	}
	first := keys[0].ID
	header := append([]byte{formatVersion, byte(len(first) & maxKeyIDLen)}, first...)
	return &Keyring{encryptingHeader: header, encryptingAEAD: aeads[first], aeads: aeads}, nil
}

func newAEAD(key config.EncryptionKey) (cipher.AEAD, error) {
	if len(key.ID) == 0 || len(key.ID) > maxKeyIDLen {
		return nil, fmt.Errorf("key id %q must be 1 to %d bytes", key.ID, maxKeyIDLen)
	}
	material := key.Material.Bytes()
	if len(material) != keyLength {
		return nil, fmt.Errorf("key %q must be %d bytes", key.ID, keyLength)
	}
	block, _ := aes.NewCipher(material)
	aead, _ := cipher.NewGCM(block)
	return aead, nil
}

func (k *Keyring) Seal(plaintext, aad []byte) []byte {
	aead := k.encryptingAEAD
	nonce := make([]byte, aead.NonceSize())
	_, _ = rand.Read(nonce)
	sealed := append(bytes.Clone(k.encryptingHeader), nonce...)
	return aead.Seal(sealed, nonce, plaintext, additionalData(k.encryptingHeader, aad))
}

func (k *Keyring) Open(ciphertext, aad []byte) ([]byte, error) {
	if len(ciphertext) < 2 || ciphertext[0] != formatVersion {
		return nil, ErrDecrypt
	}
	idEnd := 2 + int(ciphertext[1])
	if len(ciphertext) < idEnd {
		return nil, ErrDecrypt
	}
	header := ciphertext[:idEnd]
	aead, ok := k.aeads[string(header[2:])]
	if !ok {
		return nil, ErrUnknownKey
	}
	nonceEnd := idEnd + aead.NonceSize()
	if len(ciphertext) < nonceEnd+aead.Overhead() {
		return nil, ErrDecrypt
	}
	plaintext, err := aead.Open(nil, ciphertext[idEnd:nonceEnd], ciphertext[nonceEnd:], additionalData(header, aad))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

func additionalData(header, aad []byte) []byte {
	return append(append(make([]byte, 0, len(header)+len(aad)), header...), aad...)
}
