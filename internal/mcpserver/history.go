package mcpserver

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/view"
)

const (
	timelinePath       = "/api/recipes/timeline/events"
	defaultEventType   = "info"
	madeItEventSubject = "Made it"
)

var timelineFields = []string{"id", "recipeId", "subject", "eventType", "eventMessage", "timestamp"}

type historyReadInput struct {
	actionField
	Slug    string `json:"slug,omitempty" jsonschema:"timeline: only events of this recipe (slug or id)."`
	Page    int    `json:"page,omitempty" jsonschema:"timeline: page number, starting at 1."`
	PerPage int    `json:"perPage,omitempty" jsonschema:"timeline: results per page (default 20, max 100)."`
}

func addHistoryRead(srv *mcp.Server, d Deps) error {
	return addActionTool(srv, d, toolSpec{
		name:    "history_read",
		summary: "Read a recipe's timeline (when it was made, notes, comments).",
		read:    true,
	}, []action[historyReadInput]{
		{name: "timeline", help: "List timeline events, newest first. Optional: slug, page, perPage.", run: readTimeline},
	}, nil)
}

type historyWriteInput struct {
	actionField
	Slug      string         `json:"slug,omitempty" jsonschema:"add, made_it: recipe slug or id."`
	ID        string         `json:"id,omitempty" jsonschema:"update, delete: timeline event id."`
	Subject   string         `json:"subject,omitempty" jsonschema:"add: short title of the entry."`
	Message   string         `json:"message,omitempty" jsonschema:"add, made_it: longer text of the entry."`
	EventType string         `json:"eventType,omitempty" jsonschema:"add: kind of entry (default info)."`
	Timestamp string         `json:"timestamp,omitempty" jsonschema:"add, made_it: when it happened, RFC 3339 (made_it defaults to now)."`
	Fields    map[string]any `json:"fields,omitempty" jsonschema:"update: event fields to change, using Mealie's names, e.g. subject, eventMessage."`
}

func addHistoryWrite(srv *mcp.Server, d Deps) error {
	return addActionTool(srv, d, toolSpec{
		name:    "history_write",
		summary: "Log and edit recipe timeline entries. Use made_it when the user cooked a recipe.",
	}, []action[historyWriteInput]{
		{name: "add", help: "Add a timeline entry. Needs slug, subject. Optional: message, eventType, timestamp.", run: addTimelineEvent},
		{name: "made_it", help: "Record that the user cooked a recipe: sets its last-made date and adds a timeline entry. Needs slug. Optional: message, timestamp.", run: madeIt},
		{name: "update", help: "Change a timeline entry. Needs id, fields.", run: updateTimelineEvent},
		{name: "delete", help: "Delete a timeline entry. Needs id.", run: deleteTimelineEvent},
	}, map[string][]any{"eventType": {"info", "comment"}})
}

func readTimeline(ctx context.Context, s *session, in historyReadInput) (any, error) {
	query := pageQuery(in.Page, in.PerPage)
	query.Set("orderBy", "timestamp")
	query.Set("orderDirection", "desc")
	if in.Slug != "" {
		id, err := recipeID(ctx, s, in.Slug)
		if err != nil {
			return nil, err
		}
		query.Set("queryFilter", fmt.Sprintf("recipe_id=%q", id))
	}
	raw, err := s.call(ctx, http.MethodGet, timelinePath, query, nil)
	if err != nil {
		return nil, err
	}
	return view.Page(raw, timelineFields)
}

func postTimelineEvent(ctx context.Context, s *session, body map[string]string) (any, error) {
	raw, err := s.call(ctx, http.MethodPost, timelinePath, nil, body)
	if err != nil {
		return nil, err
	}
	return compactWrite(raw, timelineFields), nil
}

func timelineBody(recipe, subject, eventType string, in historyWriteInput) map[string]string {
	body := map[string]string{"recipeId": recipe, "subject": subject, "eventType": eventType}
	if in.Message != "" {
		body["eventMessage"] = in.Message
	}
	if in.Timestamp != "" {
		body["timestamp"] = in.Timestamp
	}
	return body
}

func addTimelineEvent(ctx context.Context, s *session, in historyWriteInput) (any, error) {
	if err := need("add", "slug", in.Slug); err != nil {
		return nil, err
	}
	if err := need("add", "subject", in.Subject); err != nil {
		return nil, err
	}
	id, err := recipeID(ctx, s, in.Slug)
	if err != nil {
		return nil, err
	}
	eventType := in.EventType
	if eventType == "" {
		eventType = defaultEventType
	}
	return postTimelineEvent(ctx, s, timelineBody(id, in.Subject, eventType, in))
}

func madeIt(ctx context.Context, s *session, in historyWriteInput) (any, error) {
	if err := need("made_it", "slug", in.Slug); err != nil {
		return nil, err
	}
	id, err := recipeID(ctx, s, in.Slug)
	if err != nil {
		return nil, err
	}
	in.Timestamp = s.timestampOrNow(in.Timestamp)
	if err := markMade(ctx, s, in.Slug, in.Timestamp); err != nil {
		return nil, err
	}
	event, err := postTimelineEvent(ctx, s, timelineBody(id, madeItEventSubject, defaultEventType, in))
	if err != nil {
		return nil, toolFailure(fmt.Sprintf("Marked %s as made, but adding the timeline entry failed: %s", in.Slug, err))
	}
	return event, nil
}

func updateTimelineEvent(ctx context.Context, s *session, in historyWriteInput) (any, error) {
	return updateByID(ctx, s, "update", timelinePath, in.ID, in.Fields, timelineFields)
}

func deleteTimelineEvent(ctx context.Context, s *session, in historyWriteInput) (any, error) {
	return deleteByID(ctx, s, "delete", timelinePath, in.ID)
}
