package oauth

import (
	"errors"
	"testing"
)

func TestResolveResource(t *testing.T) {
	const ours = "https://mcp.example/mcp"
	tests := []struct {
		name      string
		requested string
		ours      string
		want      string
		wantErr   error
	}{
		{"empty requested", "", ours, ours, nil},
		{"exact", ours, ours, ours, nil},
		{"requested trailing slash", ours + "/", ours, ours, nil},
		{"ours trailing slash", ours, ours + "/", ours + "/", nil},
		{"double trailing slash", ours + "//", ours, "", errInvalidTarget},
		{"other host", "https://other.example/mcp", ours, "", errInvalidTarget},
		{"other path", "https://mcp.example/other", ours, "", errInvalidTarget},
		{"sub path", ours + "/x", ours, "", errInvalidTarget},
		{"other scheme", "http://mcp.example/mcp", ours, "", errInvalidTarget},
		{"case differs", "https://MCP.example/mcp", ours, "", errInvalidTarget},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveResource(tt.requested, tt.ours)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("resource = %q, want %q", got, tt.want)
			}
		})
	}
}
