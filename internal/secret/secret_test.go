package secret

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecret_NeverPrints(t *testing.T) {
	s := FromString("hunter2")

	jsonBytes, err := json.Marshal(struct{ S Secret }{s})
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	textBytes, err := s.MarshalText()
	if err != nil {
		t.Fatalf("marshal text: %v", err)
	}
	var logLine bytes.Buffer
	slog.New(slog.NewJSONHandler(&logLine, nil)).Info("msg", "s", s)

	outputs := map[string]string{
		"Sprint":  fmt.Sprint(s),
		"Sprintf": fmt.Sprintf("%v %+v %#v %s", s, s, s, s),
		"json":    string(jsonBytes),
		"text":    string(textBytes),
		"slog":    logLine.String(),
	}
	for name, out := range outputs {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(out, "hunter2") {
				t.Errorf("output leaks the secret: %q", out)
			}
			if !strings.Contains(out, "[REDACTED]") {
				t.Errorf("output lacks the redaction marker: %q", out)
			}
		})
	}
}

func TestSecret_GoString(t *testing.T) {
	if got := FromString("hunter2").GoString(); got != "secret.Secret{[REDACTED]}" {
		t.Errorf("GoString() = %q", got)
	}
}

func TestSecret_Reveal(t *testing.T) {
	s := FromString("hunter2")
	if got := s.Reveal(); got != "hunter2" {
		t.Errorf("Reveal() = %q", got)
	}
	if got := string(s.Bytes()); got != "hunter2" {
		t.Errorf("Bytes() = %q", got)
	}
	if got := string(New([]byte("abc")).Bytes()); got != "abc" {
		t.Errorf("New().Bytes() = %q", got)
	}
}
