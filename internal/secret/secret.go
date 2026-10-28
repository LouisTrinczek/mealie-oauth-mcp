package secret

import "log/slog"

const redacted = "[REDACTED]"

type Secret struct {
	value []byte
}

func New(b []byte) Secret {
	return Secret{value: b}
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
