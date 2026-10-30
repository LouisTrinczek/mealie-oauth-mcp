package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/view"
)

const (
	mealplansPath     = "/api/households/mealplans"
	mealplanRulesPath = mealplansPath + "/rules"
	dateLayout        = "2006-01-02"
	defaultEntryType  = "dinner"
	listedDays        = 7
	maxBulkEntries    = 31
)

var (
	mealplanFields     = []string{"id", "date", "entryType", "title", "text", "recipe"}
	mealplanRuleFields = []string{"id", "day", "entryType", "queryFilterString"}
	entryTypes         = []any{"breakfast", "lunch", "dinner", "side", "snack", "drink", "dessert"}
)

type mealplansReadInput struct {
	actionField
	Start   string `json:"start,omitempty" jsonschema:"list: first date, YYYY-MM-DD (default today)."`
	End     string `json:"end,omitempty" jsonschema:"list: last date, YYYY-MM-DD (default start plus 6 days)."`
	Page    int    `json:"page,omitempty" jsonschema:"list, rules: page number, starting at 1."`
	PerPage int    `json:"perPage,omitempty" jsonschema:"list, rules: results per page (default 20, max 100)."`
}

func addMealplansRead(srv *mcp.Server, d Deps) error {
	return addActionTool(srv, d, toolSpec{
		name:    "mealplans_read",
		summary: "Read the meal plan. list covers past and future dates (history included); today is a shortcut.",
		read:    true,
	}, []action[mealplansReadInput]{
		{name: "list", help: "List planned meals, oldest first. Optional: start, end, page, perPage.", run: listMealplans},
		{name: "today", help: "List today's planned meals.", run: todaysMealplans},
		{name: "rules", help: "List the rules used to pick random meals. Optional: page, perPage.", run: listMealplanRules},
	}, nil)
}

type mealplanEntry struct {
	Date      string `json:"date,omitempty" jsonschema:"Date, YYYY-MM-DD."`
	EntryType string `json:"entryType,omitempty" jsonschema:"Meal type: breakfast, lunch, dinner, side, snack, drink or dessert (default dinner)."`
	Slug      string `json:"slug,omitempty" jsonschema:"Recipe slug or id to plan; use this or title."`
	Title     string `json:"title,omitempty" jsonschema:"Free-text meal name when there is no recipe."`
	Text      string `json:"text,omitempty" jsonschema:"Extra note for the entry."`
}

type mealplansWriteInput struct {
	actionField
	Date        string          `json:"date,omitempty" jsonschema:"create, random: date, YYYY-MM-DD."`
	EntryType   string          `json:"entryType,omitempty" jsonschema:"create, random, rule_create: meal type: breakfast, lunch, dinner, side, snack, drink or dessert (create, random default dinner)."`
	Slug        string          `json:"slug,omitempty" jsonschema:"create: recipe slug or id to plan; use this or title."`
	Title       string          `json:"title,omitempty" jsonschema:"create: free-text meal name when there is no recipe."`
	Text        string          `json:"text,omitempty" jsonschema:"create: extra note for the entry."`
	ID          string          `json:"id,omitempty" jsonschema:"update, delete, rule_update, rule_delete: entry or rule id."`
	Entries     []mealplanEntry `json:"entries,omitempty" jsonschema:"bulk_create: entries to plan, at most 31."`
	Fields      map[string]any  `json:"fields,omitempty" jsonschema:"update, rule_update: fields to change, using Mealie's names."`
	Day         string          `json:"day,omitempty" jsonschema:"rule_create: weekday the rule applies to (default unset, meaning any day)."`
	QueryFilter string          `json:"queryFilter,omitempty" jsonschema:"rule_create: Mealie recipe filter the rule picks from."`
}

func addMealplansWrite(srv *mcp.Server, d Deps) error {
	return addActionTool(srv, d, toolSpec{
		name:    "mealplans_write",
		summary: "Plan meals. Pass a recipe slug or a free-text title per entry.",
	}, []action[mealplansWriteInput]{
		{name: "create", help: "Plan one meal. Needs date and slug or title. Optional: entryType, text.", run: createMealplan},
		{name: "bulk_create", help: "Plan several meals in order, stopping at the first failure. Needs entries (at most 31).", run: bulkCreateMealplans},
		{name: "random", help: "Let Mealie pick a random meal using the rules. Needs date. Optional: entryType.", run: randomMealplan},
		{name: "update", help: "Change a planned meal. Needs id, fields.", run: updateMealplan},
		{name: "delete", help: "Remove a planned meal. Needs id.", run: deleteMealplan},
		{name: "rule_create", help: "Add a random-pick rule. Optional: day, entryType, queryFilter.", run: createMealplanRule},
		{name: "rule_update", help: "Change a random-pick rule. Needs id, fields.", run: updateMealplanRule},
		{name: "rule_delete", help: "Delete a random-pick rule. Needs id.", run: deleteMealplanRule},
	}, map[string][]any{"entryType": entryTypes})
}

func checkDate(field, value string) error {
	if _, err := time.Parse(dateLayout, value); err != nil {
		return toolFailure(field + " must be YYYY-MM-DD.")
	}
	return nil
}

func listRange(in mealplansReadInput, now time.Time) (string, string, error) {
	start := in.Start
	if start == "" {
		start = now.Format(dateLayout)
	}
	first, err := time.Parse(dateLayout, start)
	if err != nil {
		return "", "", toolFailure("start must be YYYY-MM-DD.")
	}
	end := in.End
	if end == "" {
		return start, first.AddDate(0, 0, listedDays-1).Format(dateLayout), nil
	}
	if err := checkDate("end", end); err != nil {
		return "", "", err
	}
	return start, end, nil
}

func listMealplans(ctx context.Context, s *session, in mealplansReadInput) (any, error) {
	start, end, err := listRange(in, s.clock())
	if err != nil {
		return nil, err
	}
	query := pageQuery(in.Page, in.PerPage)
	query.Set("start_date", start)
	query.Set("end_date", end)
	query.Set("orderBy", "date")
	query.Set("orderDirection", "asc")
	raw, err := s.call(ctx, http.MethodGet, mealplansPath, query, nil)
	if err != nil {
		return nil, err
	}
	return view.Page(raw, mealplanFields)
}

func todaysMealplans(ctx context.Context, s *session, _ mealplansReadInput) (any, error) {
	raw, err := s.call(ctx, http.MethodGet, mealplansPath+"/today", nil, nil)
	if err != nil {
		return nil, err
	}
	return view.List(raw, mealplanFields)
}

func listMealplanRules(ctx context.Context, s *session, in mealplansReadInput) (any, error) {
	raw, err := s.call(ctx, http.MethodGet, mealplanRulesPath, pageQuery(in.Page, in.PerPage), nil)
	if err != nil {
		return nil, err
	}
	return view.Page(raw, mealplanRuleFields)
}

func orDefaultEntryType(entryType string) string {
	if entryType == "" {
		return defaultEntryType
	}
	return entryType
}

func compactEntry(raw json.RawMessage) (any, error) {
	var entry map[string]any
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, fmt.Errorf("decoding the meal plan entry: %w", err)
	}
	return view.Compact(entry, mealplanFields), nil
}

func checkEntry(action string, e mealplanEntry) error {
	if err := need(action, "date", e.Date); err != nil {
		return err
	}
	if err := checkDate("date", e.Date); err != nil {
		return err
	}
	if e.Slug == "" && e.Title == "" {
		return toolFailure(fmt.Sprintf("Action %q needs slug or title.", action))
	}
	return nil
}

func entryBody(ctx context.Context, s *session, e mealplanEntry) (map[string]any, error) {
	body := map[string]any{"date": e.Date, "entryType": orDefaultEntryType(e.EntryType)}
	if e.Title != "" {
		body["title"] = e.Title
	}
	if e.Text != "" {
		body["text"] = e.Text
	}
	if e.Slug != "" {
		id, err := recipeID(ctx, s, e.Slug)
		if err != nil {
			return nil, err
		}
		body["recipeId"] = id
	}
	return body, nil
}

func postMealplan(ctx context.Context, s *session, e mealplanEntry) (any, error) {
	body, err := entryBody(ctx, s, e)
	if err != nil {
		return nil, err
	}
	raw, err := s.call(ctx, http.MethodPost, mealplansPath, nil, body)
	if err != nil {
		return nil, err
	}
	return compactEntry(raw)
}

func createMealplan(ctx context.Context, s *session, in mealplansWriteInput) (any, error) {
	entry := mealplanEntry{Date: in.Date, EntryType: in.EntryType, Slug: in.Slug, Title: in.Title, Text: in.Text}
	if err := checkEntry("create", entry); err != nil {
		return nil, err
	}
	return postMealplan(ctx, s, entry)
}

func dateOrNone(date string) string {
	if date == "" {
		return "no date"
	}
	return date
}

func bulkCreateMealplans(ctx context.Context, s *session, in mealplansWriteInput) (any, error) {
	if len(in.Entries) == 0 {
		return nil, need("bulk_create", "entries", "")
	}
	if len(in.Entries) > maxBulkEntries {
		return nil, toolFailure(fmt.Sprintf("bulk_create takes at most %d entries.", maxBulkEntries))
	}
	for i, e := range in.Entries {
		if err := checkEntry("bulk_create", e); err != nil {
			return nil, toolFailure(fmt.Sprintf("Entry %d (%s) is invalid: %s Nothing was created.", i+1, dateOrNone(e.Date), err))
		}
	}
	created := make([]any, 0, len(in.Entries))
	for i, e := range in.Entries {
		entry, err := postMealplan(ctx, s, e)
		if err != nil {
			return nil, toolFailure(fmt.Sprintf("Created %d of %d entries; entry %d (%s) failed: %s", i, len(in.Entries), i+1, e.Date, err))
		}
		created = append(created, entry)
	}
	return map[string]any{"items": created}, nil
}

func randomMealplan(ctx context.Context, s *session, in mealplansWriteInput) (any, error) {
	if err := need("random", "date", in.Date); err != nil {
		return nil, err
	}
	if err := checkDate("date", in.Date); err != nil {
		return nil, err
	}
	body := map[string]string{"date": in.Date, "entryType": orDefaultEntryType(in.EntryType)}
	raw, err := s.call(ctx, http.MethodPost, mealplansPath+"/random", nil, body)
	if err != nil {
		return nil, err
	}
	return compactEntry(raw)
}

func updateMealplan(ctx context.Context, s *session, in mealplansWriteInput) (any, error) {
	return updateByID(ctx, s, "update", mealplansPath, in.ID, in.Fields)
}

func deleteMealplan(ctx context.Context, s *session, in mealplansWriteInput) (any, error) {
	return deleteByID(ctx, s, "delete", mealplansPath, in.ID)
}

func updateMealplanRule(ctx context.Context, s *session, in mealplansWriteInput) (any, error) {
	return updateByID(ctx, s, "rule_update", mealplanRulesPath, in.ID, in.Fields)
}

func deleteMealplanRule(ctx context.Context, s *session, in mealplansWriteInput) (any, error) {
	return deleteByID(ctx, s, "rule_delete", mealplanRulesPath, in.ID)
}

func createMealplanRule(ctx context.Context, s *session, in mealplansWriteInput) (any, error) {
	day := in.Day
	if day == "" {
		day = "unset"
	}
	body := map[string]string{"day": day, "queryFilterString": in.QueryFilter}
	if in.EntryType != "" {
		body["entryType"] = in.EntryType
	}
	raw, err := s.call(ctx, http.MethodPost, mealplanRulesPath, nil, body)
	if err != nil {
		return nil, err
	}
	var rule map[string]any
	if err := json.Unmarshal(raw, &rule); err != nil {
		return nil, fmt.Errorf("decoding the meal plan rule: %w", err)
	}
	return view.Compact(rule, mealplanRuleFields), nil
}
