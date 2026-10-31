package tracing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type retainingExporter struct{ *tracetest.InMemoryExporter }

func (retainingExporter) Shutdown(context.Context) error { return nil }

func recordingProvider() (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	exporter := tracetest.NewInMemoryExporter()
	return sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter)), exporter
}

func serve(t *testing.T, tp trace.TracerProvider, pattern string, status int, target string, header http.Header) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
	handler := Middleware(tp)(mux)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	for key, values := range header {
		req.Header[key] = values
	}
	handler.ServeHTTP(httptest.NewRecorder(), req)
}

func TestMiddleware_NamesSpanByRouteWithoutQuery(t *testing.T) {
	tp, exporter := recordingProvider()
	serve(t, tp, "GET /callback", http.StatusOK, "/callback?code=SECRET-CODE&state=S", nil)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	span := spans[0]
	if span.Name != "GET /callback" {
		t.Errorf("name = %q", span.Name)
	}
	if span.SpanKind != trace.SpanKindServer {
		t.Errorf("kind = %v", span.SpanKind)
	}
	got := map[attribute.Key]string{}
	for _, attr := range span.Attributes {
		got[attr.Key] = attr.Value.String()
		if strings.Contains(attr.Value.String(), "SECRET-CODE") || strings.Contains(attr.Value.String(), "state=") {
			t.Errorf("attribute %s leaks request data: %s", attr.Key, attr.Value.String())
		}
	}
	want := map[attribute.Key]string{
		"http.request.method":       "GET",
		"http.route":                "GET /callback",
		"http.response.status_code": "200",
	}
	if len(got) != len(want) {
		t.Errorf("attributes = %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
}

func TestMiddleware_ContinuesIncomingTrace(t *testing.T) {
	tp, exporter := recordingProvider()
	header := http.Header{"Traceparent": {"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}}
	serve(t, tp, "GET /x", http.StatusOK, "/x", header)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	if got := spans[0].SpanContext.TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace id = %s", got)
	}
	if got := spans[0].Parent.SpanID().String(); got != "00f067aa0ba902b7" {
		t.Errorf("parent span id = %s", got)
	}
}

func TestMiddleware_MarksServerErrors(t *testing.T) {
	tests := []struct {
		status int
		want   codes.Code
	}{
		{http.StatusInternalServerError, codes.Error},
		{http.StatusNotFound, codes.Unset},
	}
	for _, tt := range tests {
		tp, exporter := recordingProvider()
		serve(t, tp, "GET /x", tt.status, "/x", nil)
		spans := exporter.GetSpans()
		if len(spans) != 1 {
			t.Fatalf("status %d: spans = %d, want 1", tt.status, len(spans))
		}
		if got := spans[0].Status.Code; got != tt.want {
			t.Errorf("status %d: code = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestSetup_DisabledIsNoop(t *testing.T) {
	called := false
	export := func(context.Context) (sdktrace.SpanExporter, error) {
		called = true
		return nil, nil
	}
	tp, shutdown, err := Setup(context.Background(), false, "1.2.3", export, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	_, span := Tracer(tp).Start(context.Background(), "op")
	if span.IsRecording() {
		t.Error("span is recording")
	}
	span.End()
	if called {
		t.Error("exporter was called")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

func TestSetup_ExporterFailure(t *testing.T) {
	export := func(context.Context) (sdktrace.SpanExporter, error) { return nil, errors.New("boom") }
	_, _, err := Setup(context.Background(), true, "1.2.3", export, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "starting OTLP exporter: boom") {
		t.Errorf("err = %v", err)
	}
}

func TestSetup_ExportsWithServiceResource(t *testing.T) {
	memory := tracetest.NewInMemoryExporter()
	export := func(context.Context) (sdktrace.SpanExporter, error) { return retainingExporter{memory}, nil }
	tp, shutdown, err := Setup(context.Background(), true, "1.2.3", export, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	_, span := Tracer(tp).Start(context.Background(), "op")
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	spans := memory.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	got := map[attribute.Key]string{}
	for _, attr := range spans[0].Resource.Attributes() {
		got[attr.Key] = attr.Value.String()
	}
	if got["service.name"] != "mealie-oauth-mcp" || got["service.version"] != "1.2.3" {
		t.Errorf("resource = %v", got)
	}
}

func TestSetup_UnreachableCollectorDoesNotBlock(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	tp, shutdown, err := Setup(context.Background(), true, "t", OTLP, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	_, span := Tracer(tp).Start(context.Background(), "op")
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_ = shutdown(ctx)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("shutdown took %v", elapsed)
	}
}

func TestMiddleware_DefaultsStatusToOKWhenHandlerNeverWritesHeader(t *testing.T) {
	tp, exporter := recordingProvider()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /body", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hi")) })
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/body", nil)
	Middleware(tp)(mux).ServeHTTP(httptest.NewRecorder(), req)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	if spans[0].Status.Code != codes.Unset {
		t.Errorf("code = %v, want Unset", spans[0].Status.Code)
	}
	found := false
	for _, attr := range spans[0].Attributes {
		if attr.Key == "http.response.status_code" {
			found = true
			if attr.Value.AsInt64() != http.StatusOK {
				t.Errorf("status_code = %d, want 200", attr.Value.AsInt64())
			}
		}
	}
	if !found {
		t.Error("http.response.status_code attribute missing")
	}
}

func TestMiddleware_MalformedTraceparentStartsRootSpan(t *testing.T) {
	tp, exporter := recordingProvider()
	serve(t, tp, "GET /x", http.StatusOK, "/x", http.Header{"Traceparent": {"garbage"}})

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	if spans[0].Parent.IsValid() {
		t.Errorf("parent = %v, want none", spans[0].Parent)
	}
	if !spans[0].SpanContext.TraceID().IsValid() {
		t.Error("trace id is not valid")
	}
}

type failingExporter struct{ retainingExporter }

func (failingExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return errors.New("collector refused connection")
}

func TestSetup_LogsExportFailuresThroughLogger(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	export := func(context.Context) (sdktrace.SpanExporter, error) {
		return failingExporter{retainingExporter{tracetest.NewInMemoryExporter()}}, nil
	}
	tp, shutdown, err := Setup(context.Background(), true, "1.2.3", export, logger)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	_, span := Tracer(tp).Start(context.Background(), "span-content-marker")
	span.End()

	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("want one JSON log line, got %q: %v", logs.String(), err)
	}
	if entry["level"] != "WARN" || entry["msg"] != "exporting traces failed" || entry["error"] != "collector refused connection" {
		t.Errorf("log entry = %v", entry)
	}
	if strings.Contains(logs.String(), "span-content-marker") {
		t.Errorf("log leaks span content: %s", logs.String())
	}
}
