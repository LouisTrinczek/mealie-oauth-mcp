package mcpserver

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type surfaceProperty struct {
	Type       any                        `json:"type,omitempty"`
	Enum       []any                      `json:"enum,omitempty"`
	Items      *surfaceProperty           `json:"items,omitempty"`
	Properties map[string]surfaceProperty `json:"properties,omitempty"`
	Required   []string                   `json:"required,omitempty"`
}

type surfaceInput struct {
	Properties map[string]surfaceProperty `json:"properties"`
	Required   []string                   `json:"required"`
}

type surfaceTool struct {
	Name        string       `json:"name"`
	ReadOnly    bool         `json:"readOnly"`
	Destructive bool         `json:"destructive"`
	Input       surfaceInput `json:"input"`
}

func toolSurface(tools []*mcp.Tool) ([]byte, error) {
	surface := make([]surfaceTool, 0, len(tools))
	for _, tool := range tools {
		input, err := reduceInputSchema(tool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("reducing input schema of %s: %w", tool.Name, err)
		}
		entry := surfaceTool{Name: tool.Name, Input: input}
		if tool.Annotations != nil {
			entry.ReadOnly = tool.Annotations.ReadOnlyHint
			entry.Destructive = tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint
		}
		surface = append(surface, entry)
	}
	slices.SortFunc(surface, func(a, b surfaceTool) int { return strings.Compare(a.Name, b.Name) })
	return json.MarshalIndent(surface, "", "  ")
}

func reduceInputSchema(schema any) (surfaceInput, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return surfaceInput{}, fmt.Errorf("marshaling schema: %w", err)
	}
	var input surfaceInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return surfaceInput{}, fmt.Errorf("decoding schema: %w", err)
	}
	if input.Properties == nil {
		input.Properties = map[string]surfaceProperty{}
	}
	if input.Required == nil {
		input.Required = []string{}
	}
	slices.Sort(input.Required)
	sortNestedRequired(input.Properties)
	return input, nil
}

func sortNestedRequired(properties map[string]surfaceProperty) {
	for name, property := range properties {
		properties[name] = withSortedRequired(property)
	}
}

func withSortedRequired(property surfaceProperty) surfaceProperty {
	slices.Sort(property.Required)
	sortNestedRequired(property.Properties)
	if property.Items != nil {
		items := withSortedRequired(*property.Items)
		property.Items = &items
	}
	return property
}

func listAllTools(t *testing.T, cs *mcp.ClientSession) []*mcp.Tool {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	return res.Tools
}

func TestToolSurface_MatchesGolden(t *testing.T) {
	cs := connectTools(t, newFakeMealie(t))

	got, err := toolSurface(listAllTools(t, cs))
	if err != nil {
		t.Fatalf("toolSurface: %v", err)
	}

	assertGolden(t, "tool_surface", string(got)+"\n")
}

func surfaceFixture(toolDescription, propertyDescription, actionValue string) []*mcp.Tool {
	return []*mcp.Tool{{
		Name:        "recipes_read",
		Description: toolDescription,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"action"},
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "description": propertyDescription, "enum": []any{actionValue, "search"}},
			},
		},
	}}
}

func TestToolSurface_IgnoresDescriptions(t *testing.T) {
	tests := []struct {
		name      string
		other     []*mcp.Tool
		wantEqual bool
	}{
		{"different descriptions", surfaceFixture("changed tool text", "changed property text", "get"), true},
		{"different enum value", surfaceFixture("tool text", "property text", "fetch"), false},
	}
	base, err := toolSurface(surfaceFixture("tool text", "property text", "get"))
	if err != nil {
		t.Fatalf("toolSurface: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			other, err := toolSurface(tt.other)
			if err != nil {
				t.Fatalf("toolSurface: %v", err)
			}
			if equal := string(base) == string(other); equal != tt.wantEqual {
				t.Errorf("surfaces equal = %v, want %v", equal, tt.wantEqual)
			}
		})
	}
}

func nestedSurfaceFixture(itemField, itemDescription string, required []any) []*mcp.Tool {
	return []*mcp.Tool{{
		Name: "shopping_write",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"items": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":     "object",
						"required": required,
						"properties": map[string]any{
							itemField: map[string]any{"type": "boolean", "description": itemDescription},
							"note":    map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	}}
}

func TestToolSurface_TracksNestedItemFields(t *testing.T) {
	tests := []struct {
		name      string
		other     []*mcp.Tool
		wantEqual bool
	}{
		{"different nested description", nestedSurfaceFixture("checked", "changed item text", []any{"checked", "note"}), true},
		{"reordered nested required", nestedSurfaceFixture("checked", "item text", []any{"note", "checked"}), true},
		{"renamed nested field", nestedSurfaceFixture("done", "item text", []any{"done", "note"}), false},
	}
	base, err := toolSurface(nestedSurfaceFixture("checked", "item text", []any{"checked", "note"}))
	if err != nil {
		t.Fatalf("toolSurface: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			other, err := toolSurface(tt.other)
			if err != nil {
				t.Fatalf("toolSurface: %v", err)
			}
			if equal := string(base) == string(other); equal != tt.wantEqual {
				t.Errorf("surfaces equal = %v, want %v", equal, tt.wantEqual)
			}
		})
	}
}

func TestToolSurface_RejectsUnreadableSchemas(t *testing.T) {
	tests := []struct {
		name   string
		schema any
	}{
		{"unmarshalable schema", make(chan int)},
		{"malformed properties", map[string]any{"properties": "not an object"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := toolSurface([]*mcp.Tool{{Name: "broken", InputSchema: tt.schema}}); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestToolSurface_SortsByNameAndDefaultsMissingAnnotations(t *testing.T) {
	destructive := true
	got, err := toolSurface([]*mcp.Tool{
		{Name: "b_write", Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive}, InputSchema: map[string]any{"type": "object"}},
		{Name: "a_plain", InputSchema: map[string]any{"type": "object"}},
	})
	if err != nil {
		t.Fatalf("toolSurface: %v", err)
	}
	var decoded []surfaceTool
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded[0].Name != "a_plain" || decoded[0].Destructive || decoded[1].Name != "b_write" || !decoded[1].Destructive {
		t.Errorf("unexpected surface: %+v", decoded)
	}
}
