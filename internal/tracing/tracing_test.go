package tracing

import (
	"context"
	"errors"
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
	tp, shutdown, err := Setup(context.Background(), false, "1.2.3", export)
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
	_, _, err := Setup(context.Background(), true, "1.2.3", export)
	if err == nil || !strings.Contains(err.Error(), "starting OTLP exporter: boom") {
		t.Errorf("err = %v", err)
	}
}

func TestSetup_ExportsWithServiceResource(t *testing.T) {
	memory := tracetest.NewInMemoryExporter()
	export := func(context.Context) (sdktrace.SpanExporter, error) { return retainingExporter{memory}, nil }
	tp, shutdown, err := Setup(context.Background(), true, "1.2.3", export)
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
	tp, shutdown, err := Setup(context.Background(), true, "t", OTLP)
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
