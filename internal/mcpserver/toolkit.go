package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/view"
)

const errRequestFailed toolFailure = "Mealie request failed. Try again."

type session struct {
	tokens TokenSource
	mealie MealieClient
	clock  func() time.Time
	info   *auth.TokenInfo
	token  secret.Secret
}

func openSession(ctx context.Context, d Deps, req *mcp.CallToolRequest) (*session, error) {
	info := tokenInfo(req)
	tok, err := d.Tokens.MealieToken(ctx, info)
	if err != nil {
		return nil, tokenFailure(err)
	}
	return &session{tokens: d.Tokens, mealie: d.Mealie, clock: d.Clock, info: info, token: tok}, nil
}

func (s *session) call(ctx context.Context, method, path string, q url.Values, body any) (json.RawMessage, error) {
	raw, err := s.mealie.Call(ctx, s.token, method, path, q, body)
	if err != nil {
		return nil, s.failure(ctx, err)
	}
	return raw, nil
}

func (s *session) self(ctx context.Context) (mealie.User, error) {
	user, err := s.mealie.Self(ctx, s.token)
	if err != nil {
		return mealie.User{}, s.failure(ctx, err)
	}
	return user, nil
}

func (s *session) failure(ctx context.Context, err error) error {
	var segment *mealie.SegmentError
	var apiErr *mealie.APIError
	switch {
	case mealie.IsUnauthorized(err):
		s.tokens.ReportUnauthorized(ctx, s.info)
		return errSessionEnded
	case mealie.IsUnavailable(err):
		return errMealieUnavailable
	case errors.As(err, &segment):
		return toolFailure(segment.Error())
	case errors.As(err, &apiErr):
		return apiFailure(apiErr)
	default:
		return errRequestFailed
	}
}

func apiFailure(err *mealie.APIError) toolFailure {
	switch err.Status {
	case http.StatusNotFound:
		return toolFailure(fmt.Sprintf("Mealie could not find that (404): %s. Check the slug or id; search or list to find it.", err.Message))
	case http.StatusForbidden:
		return toolFailure(fmt.Sprintf("Your Mealie user is not allowed to do this (403): %s.", err.Message))
	case http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity:
		return toolFailure(fmt.Sprintf("Mealie rejected the request (%d): %s. Fix the arguments and try again.", err.Status, err.Message))
	default:
		return toolFailure(fmt.Sprintf("Mealie returned an error (%d): %s.", err.Status, err.Message))
	}
}

type action[In any] struct {
	name string
	help string
	run  func(ctx context.Context, s *session, in In) (any, error)
}

type toolSpec struct {
	name      string
	summary   string
	read      bool
	openWorld bool
}

func (spec toolSpec) annotations() *mcp.ToolAnnotations {
	if spec.read {
		return &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(spec.openWorld)}
	}
	return &mcp.ToolAnnotations{DestructiveHint: new(true), OpenWorldHint: new(spec.openWorld)}
}

type actionInput interface {
	actionName() string
}

type actionField struct {
	Action string `json:"action" jsonschema:"Which action to run; see the tool description."`
}

func (f actionField) actionName() string {
	return f.Action
}

func inputSchema[In any](enums map[string][]any) (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		return nil, fmt.Errorf("inferring %T input schema: %w", *new(In), err)
	}
	setEnums(schema, enums)
	return schema, nil
}

func setEnums(schema *jsonschema.Schema, enums map[string][]any) {
	if schema == nil {
		return
	}
	for name, property := range schema.Properties {
		if choices, found := enums[name]; found {
			property.Enum = choices
		}
		setEnums(property, enums)
		setEnums(property.Items, enums)
	}
}

func addActionTool[In actionInput](srv *mcp.Server, d Deps, spec toolSpec, actions []action[In], enums map[string][]any) error {
	names := make([]any, 0, len(actions))
	byName := make(map[string]action[In], len(actions))
	var description strings.Builder
	description.WriteString(spec.summary + "\n\nActions:")
	for _, a := range actions {
		names = append(names, a.name)
		byName[a.name] = a
		description.WriteString("\n- " + a.name + ": " + a.help)
	}
	choices := map[string][]any{"action": names}
	maps.Copy(choices, enums)
	schema, err := inputSchema[In](choices)
	if err != nil {
		return err
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        spec.name,
		Description: description.String(),
		InputSchema: schema,
		Annotations: spec.annotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		s, err := openSession(ctx, d, req)
		if err != nil {
			return nil, nil, err
		}
		result, err := byName[in.actionName()].run(ctx, s, in)
		if err != nil {
			return nil, nil, err
		}
		return textResult(result)
	})
	return nil
}

func textResult(value any) (*mcp.CallToolResult, any, error) {
	encoded, err := view.Encode(value)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil, nil
}

func need(action, field, value string) error {
	if value == "" {
		return toolFailure(fmt.Sprintf("Action %q needs %s.", action, field))
	}
	return nil
}

func pageQuery(page, perPage int) url.Values {
	return url.Values{
		"page":    {strconv.Itoa(view.PageNumber(page))},
		"perPage": {strconv.Itoa(view.PerPage(perPage))},
	}
}

func mergedObject(ctx context.Context, s *session, path string, fields map[string]any) (map[string]any, error) {
	raw, err := s.call(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	var current map[string]json.RawMessage
	if err := json.Unmarshal(raw, &current); err != nil {
		return nil, fmt.Errorf("reading %s before the update: %w", path, err)
	}
	merged := make(map[string]any, len(current)+len(fields))
	for key, value := range current {
		merged[key] = value
	}
	maps.Copy(merged, fields)
	return merged, nil
}

func mergeUpdate(ctx context.Context, s *session, path string, fields map[string]any) (json.RawMessage, error) {
	merged, err := mergedObject(ctx, s, path, fields)
	if err != nil {
		return nil, err
	}
	return s.call(ctx, http.MethodPut, path, nil, merged)
}

func trimmed(raw json.RawMessage, full bool) (any, error) {
	if full {
		return raw, nil
	}
	return view.Trim(raw)
}

func ok(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]bool{"ok": true}
	}
	if trimmedBody, err := view.Trim(raw); err == nil {
		return trimmedBody
	}
	return raw
}

func updateRaw(ctx context.Context, s *session, action, prefix, id string, fields map[string]any) (json.RawMessage, error) {
	if err := need(action, "id", id); err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, need(action, "fields", "")
	}
	path, err := mealie.PathOf(prefix, id)
	if err != nil {
		return nil, err
	}
	return mergeUpdate(ctx, s, path, fields)
}

func updateByID(ctx context.Context, s *session, action, prefix, id string, fields map[string]any) (any, error) {
	raw, err := updateRaw(ctx, s, action, prefix, id, fields)
	if err != nil {
		return nil, err
	}
	return ok(raw), nil
}

func deleteByID(ctx context.Context, s *session, action, prefix, id string) (any, error) {
	if err := need(action, "id", id); err != nil {
		return nil, err
	}
	path, err := mealie.PathOf(prefix, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.call(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return nil, err
	}
	return ok(nil), nil
}

func setIfNotEmpty(query url.Values, key, value string) {
	if value != "" {
		query.Set(key, value)
	}
}

func createCompact(ctx context.Context, s *session, kindName, base string, body any, fields []string) (any, error) {
	raw, err := s.call(ctx, http.MethodPost, base, nil, body)
	if err != nil {
		return nil, err
	}
	var created map[string]any
	if err := json.Unmarshal(raw, &created); err != nil {
		return nil, fmt.Errorf("decoding the created %s: %w", kindName, err)
	}
	return view.Compact(created, fields), nil
}
