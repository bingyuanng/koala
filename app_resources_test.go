package koala_test

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	koala "github.com/bingyuanng/koala"
	"github.com/bingyuanng/koala/config"
	"github.com/bingyuanng/koala/observability"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func TestExplicitApplicationResources(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	providers := &observability.Providers{Traces: tracenoop.NewTracerProvider(), Metrics: metricnoop.NewMeterProvider()}
	for _, tt := range []struct {
		options koala.Options
		want    string
	}{
		{koala.Options{Telemetry: providers}, "Logger is required"},
		{koala.Options{Logger: logger}, "Telemetry"},
		{koala.Options{Logger: logger, Telemetry: &observability.Providers{Traces: providers.Traces}}, "Telemetry"},
		{koala.Options{Logger: logger, Telemetry: &observability.Providers{Metrics: providers.Metrics}}, "Telemetry"},
	} {
		if _, err := koala.New(config.DefaultConfig().Server, withRequests(tt.options)); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("missing resources: %v", err)
		}
	}
	cfg := config.DefaultConfig()
	app, err := koala.New(cfg.Server, koala.Options{Requests: cfg.Requests, Logger: logger, Telemetry: providers})
	if err != nil {
		t.Fatal(err)
	}
	if app.Logger() != logger {
		t.Fatal("runtime replaced the application-owned logger")
	}
}

func withRequests(options koala.Options) koala.Options {
	options.Requests = config.DefaultConfig().Requests
	return options
}
