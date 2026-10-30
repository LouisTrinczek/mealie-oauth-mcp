package view

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	MaxBytes       = 64 << 10
	DefaultPerPage = 20
	MaxPerPage     = 100
	maxTextRunes   = 160
)

var ErrTooLarge = errors.New("result too large")

type TooLargeError struct {
	Size int
}

func (e *TooLargeError) Error() string {
	kib := (e.Size + 1023) / 1024
	return fmt.Sprintf("The result is too large (%d KiB). Use a smaller perPage, a narrower filter, or leave out full.", kib)
}

func (e *TooLargeError) Is(target error) bool {
	return target == ErrTooLarge
}

var bookkeepingKeys = map[string]bool{
	"groupId":     true,
	"householdId": true,
	"userId":      true,
	"createdAt":   true,
	"updatedAt":   true,
	"update_at":   true,
	"dateUpdated": true,
	"cacheKey":    true,
}

var referenceKeys = []string{"id", "slug", "name", "username"}

var labelKeys = []string{"name", "slug", "id"}

func Trim(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decoding object: %w", err)
	}
	return json.Marshal(trimmed(value))
}

func trimmed(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if bookkeepingKeys[key] {
				continue
			}
			if cleaned := trimmed(child); !isEmpty(cleaned) {
				out[key] = cleaned
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			if cleaned := trimmed(child); !isEmpty(cleaned) {
				out = append(out, cleaned)
			}
		}
		return out
	default:
		return value
	}
}

func isEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

func Compact(item map[string]any, fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, field := range fields {
		if reduced := reduce(item[field]); !isEmpty(reduced) {
			out[field] = reduced
		}
	}
	return out
}

func reduce(value any) any {
	switch typed := value.(type) {
	case string:
		return truncate(typed)
	case map[string]any:
		return pick(typed, referenceKeys)
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			if object, ok := child.(map[string]any); ok {
				child = label(object)
			}
			if !isEmpty(child) {
				out = append(out, child)
			}
		}
		return out
	default:
		return value
	}
}

func truncate(text string) string {
	runes := []rune(text)
	if len(runes) <= maxTextRunes {
		return text
	}
	return string(runes[:maxTextRunes]) + "…"
}

func pick(object map[string]any, keys []string) map[string]any {
	out := make(map[string]any, len(keys))
	for _, key := range keys {
		if value := object[key]; !isEmpty(value) {
			out[key] = value
		}
	}
	return out
}

func label(object map[string]any) any {
	for _, key := range labelKeys {
		if value := object[key]; !isEmpty(value) {
			return value
		}
	}
	return nil
}

func Page(raw json.RawMessage, fields []string) (map[string]any, error) {
	var page struct {
		Page       int   `json:"page"`
		PerPage    int   `json:"per_page"`
		Total      int   `json:"total"`
		TotalPages int   `json:"total_pages"`
		Items      []any `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, fmt.Errorf("decoding page: %w", err)
	}
	items, err := compactItems(page.Items, fields)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"items":      items,
		"page":       page.Page,
		"perPage":    page.PerPage,
		"total":      page.Total,
		"totalPages": page.TotalPages,
		"hasMore":    page.Page < page.TotalPages,
	}, nil
}

func List(raw json.RawMessage, fields []string) (map[string]any, error) {
	var list []any
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decoding list: %w", err)
	}
	items, err := compactItems(list, fields)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "total": len(items)}, nil
}

func compactItems(raw []any, fields []string) ([]any, error) {
	items := make([]any, 0, len(raw))
	for index, entry := range raw {
		object, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("item %d is not an object", index)
		}
		items = append(items, Compact(object, fields))
	}
	return items, nil
}

func Encode(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encoding result: %w", err)
	}
	if len(encoded) > MaxBytes {
		return nil, &TooLargeError{Size: len(encoded)}
	}
	return encoded, nil
}

func PerPage(n int) int {
	if n <= 0 {
		return DefaultPerPage
	}
	return min(n, MaxPerPage)
}

func PageNumber(n int) int {
	return max(n, 1)
}
