package observability_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bingyuanng/koala/config"
	"github.com/bingyuanng/koala/observability"
	"go.opentelemetry.io/otel/trace"
)

func TestContextualBusinessLogsWithoutExporter(t *testing.T) {
	var out bytes.Buffer
	c := config.DefaultConfig()
	c.Telemetry.Traces.Enabled = false
	p, e := observability.NewTelemetry(context.Background(), c.Telemetry, c.App, &out)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Shutdown(context.Background())
	logger := slog.New(observability.Handler{Handler: slog.NewJSONHandler(&out, nil)}).With("service", "business")
	ctx, span := p.Traces.Tracer("test").Start(context.Background(), "operation")
	defer span.End()
	logger.InfoContext(ctx, "inside request")
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() || !strings.Contains(out.String(), sc.TraceID().String()) {
		t.Fatal("missing trace correlation")
	}
	out.Reset()
	logger.InfoContext(context.Background(), "unrelated")
	if strings.Contains(out.String(), "trace_id") {
		t.Fatal("context leaked")
	}
}

func TestStdoutSpansFlushWithinBudget(t *testing.T) {
	var out bytes.Buffer
	c := config.DefaultConfig()
	c.Telemetry.Traces.Enabled = true
	p, e := observability.NewTelemetry(context.Background(), c.Telemetry, c.App, &out)
	if e != nil {
		t.Fatal(e)
	}
	_, span := p.Traces.Tracer("test").Start(context.Background(), "flush-proof")
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e = p.Shutdown(ctx); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), "flush-proof") {
		t.Fatal("span did not flush")
	}
}
func TestOutboundHTTPContextAndCancellation(t *testing.T) {
	var out bytes.Buffer
	c := config.DefaultConfig()
	c.Telemetry.Traces.Enabled = false
	p, e := observability.NewTelemetry(context.Background(), c.Telemetry, c.App, &out)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Shutdown(context.Background())
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("Traceparent")
		w.WriteHeader(200)
	}))
	defer server.Close()
	ctx, span := p.Traces.Tracer("test").Start(context.Background(), "business")
	defer span.End()
	request, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	response, e := p.HTTPClient().Do(request)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if !strings.Contains(<-received, span.SpanContext().TraceID().String()) {
		t.Fatal("outbound trace not propagated")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	request, _ = http.NewRequestWithContext(canceled, "GET", server.URL, nil)
	if _, e = p.HTTPClient().Do(request); !errors.Is(e, context.Canceled) {
		t.Fatal("outbound cancellation lost", e)
	}
}
