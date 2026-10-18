package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/bingyuanng/koala/config"
	"github.com/bingyuanng/koala/observability"
	"go.opentelemetry.io/otel/trace"
)

func TestNewLoggerOutputAndContext(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.App.Name = "logger-test"
	cfg.Logging.Level = "warn"
	var output bytes.Buffer
	global := slog.Default()
	logger, err := observability.NewLogger(cfg.Logging, &output)
	if err != nil {
		t.Fatal(err)
	}
	logger = logger.With("app", cfg.App.Name, "version", cfg.App.Version, "environment", cfg.App.Environment)
	if slog.Default() != global {
		t.Fatal("global logger changed")
	}
	logger.Info("filtered")
	if output.Len() != 0 {
		t.Fatal("ignored configured log level")
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	logger.With("component", "business").WarnContext(ctx, "request")
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"app": cfg.App.Name, "version": cfg.App.Version, "environment": cfg.App.Environment,
		"trace_id": sc.TraceID().String(), "span_id": sc.SpanID().String(),
		"component": "business", "level": "WARN", "msg": "request",
	} {
		if record[key] != want {
			t.Errorf("%s: got %v, want %s", key, record[key], want)
		}
	}
	output.Reset()
	logger.Warn("outside request")
	// Decode into a fresh map so previous attributes cannot persist in the test.
	record = nil
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if _, ok := record["trace_id"]; ok {
		t.Fatal("trace context leaked")
	}
}

func TestNewLoggerRejectsInvalidOptions(t *testing.T) {
	cfg := config.DefaultConfig()
	if logger, err := observability.NewLogger(cfg.Logging, nil); err == nil || logger != nil {
		t.Fatal("nil output accepted")
	}
	cfg.Logging.Level = "invalid"
	if logger, err := observability.NewLogger(cfg.Logging, io.Discard); err == nil || logger != nil {
		t.Fatal("invalid level accepted")
	}
}
