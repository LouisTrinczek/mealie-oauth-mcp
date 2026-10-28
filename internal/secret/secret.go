package secret

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
)

const redacted = "[REDACTED]"

type Secret struct {
	value []byte
}

func New(b []byte) Secret {
	return Secret{value: bytes.Clone(b)}
}

func FromString(s string) Secret {
	return Secret{value: []byte(s)}
}

func (s Secret) Bytes() []byte {
	return s.value
}

func (s Secret) Reveal() string {
	return string(s.value)
}

func (Secret) String() string {
	return redacted
}

func (s Secret) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		_, _ = io.WriteString(f, s.GoString())
		return
	}
	_, _ = io.WriteString(f, redacted)
}

func (Secret) GoString() string {
	return "secret.Secret{" + redacted + "}"
}

func (Secret) MarshalJSON() ([]byte, error) {
	return []byte(`"` + redacted + `"`), nil
}

func (Secret) MarshalText() ([]byte, error) {
	return []byte(redacted), nil
}

func (Secret) LogValue() slog.Value {
	return slog.StringValue(redacted)
}
