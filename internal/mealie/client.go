package mealie

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
)

const (
	defaultTimeout   = 15 * time.Second
	defaultUserAgent = "mealie-oauth-mcp/dev"
	maxResponseBytes = 10 << 20
	maxMessageRunes  = 200
	maxValidationMsg = 3
	redactedMarker   = "[REDACTED]"
	redirectMessage  = "Mealie redirected the request. Check that MEALIE_URL points directly at Mealie."
)

var ErrUnavailable = errors.New("mealie unavailable, try again")

var (
	defaultBackoff = []time.Duration{200 * time.Millisecond, 600 * time.Millisecond}
	bearerPattern  = regexp.MustCompile(`(?i)bearer\s+\S+`)
	jwtPattern     = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
)

type Options struct {
	HTTPClient *http.Client
	UserAgent  string
}

type Client struct {
	baseURL   *url.URL
	http      *http.Client
	userAgent string
	backoff   []time.Duration
}

func New(baseURL *url.URL, opts Options) *Client {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout, CheckRedirect: refuseRedirect}
	}
	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	return &Client{baseURL: baseURL, http: httpClient, userAgent: userAgent, backoff: defaultBackoff}
}

func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("mealie: status %d: %s", e.Status, e.Message)
}

func IsUnauthorized(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized
}

func IsUnavailable(err error) bool {
	if errors.Is(err, ErrUnavailable) {
		return true
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= http.StatusInternalServerError
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var netErr net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr)
}

func redact(s string) string {
	return jwtPattern.ReplaceAllString(bearerPattern.ReplaceAllString(s, redactedMarker), redactedMarker)
}

func (c *Client) do(ctx context.Context, tok secret.Secret, method, path string, query url.Values, body any, out any) error {
	raw, err := c.request(ctx, tok, method, path, query, body)
	if err != nil || out == nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decoding mealie response: %w", err)
	}
	return nil
}

func (c *Client) request(ctx context.Context, tok secret.Secret, method, path string, query url.Values, body any) ([]byte, error) {
	payload, err := encodeBody(body)
	if err != nil {
		return nil, err
	}
	target := c.baseURL.JoinPath(path)
	target.RawQuery = query.Encode()

	attempts := 1
	if method == http.MethodGet {
		attempts += len(c.backoff)
	}
	for attempt := 0; ; attempt++ {
		raw, err := c.attempt(ctx, tok, method, target.String(), payload)
		if err == nil || attempt+1 >= attempts || !IsUnavailable(err) {
			return raw, err
		}
		if err := sleep(ctx, c.backoff[attempt]); err != nil {
			return nil, err
		}
	}
}

func (c *Client) Call(ctx context.Context, tok secret.Secret, method, path string, query url.Values, body any) (json.RawMessage, error) {
	raw, err := c.request(ctx, tok, method, path, query, body)
	if err != nil {
		return nil, fmt.Errorf("calling mealie %s %s: %w", method, path, err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	return raw, nil
}

func encodeBody(body any) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding mealie request: %w", err)
	}
	return payload, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) attempt(ctx context.Context, tok secret.Secret, method, target string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("building mealie request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token := tok.Reveal(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling mealie: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading mealie response: %w", err)
	}
	if len(raw) > maxResponseBytes {
		return nil, fmt.Errorf("mealie response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, &APIError{Status: resp.StatusCode, Message: summarize(resp.StatusCode, raw)}
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return nil, &APIError{Status: resp.StatusCode, Message: redirectMessage}
	}
	return raw, nil
}

type errorBody struct {
	Detail json.RawMessage `json:"detail"`
}

type validationDetail struct {
	Msg string `json:"msg"`
}

func summarize(status int, raw []byte) string {
	var body errorBody
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Detail) == 0 {
		return http.StatusText(status)
	}
	message := detailMessage(status, body.Detail)
	if message == "" {
		return http.StatusText(status)
	}
	return truncate(redact(message), maxMessageRunes)
}

func detailMessage(status int, detail json.RawMessage) string {
	if status == http.StatusUnprocessableEntity {
		var entries []validationDetail
		if err := json.Unmarshal(detail, &entries); err == nil {
			return joinMessages(entries)
		}
	}
	var text string
	if err := json.Unmarshal(detail, &text); err == nil {
		return text
	}
	return string(detail)
}

func joinMessages(entries []validationDetail) string {
	var msgs []string
	for _, entry := range entries {
		if entry.Msg == "" {
			continue
		}
		msgs = append(msgs, entry.Msg)
		if len(msgs) == maxValidationMsg {
			break
		}
	}
	return strings.Join(msgs, "; ")
}

func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}
