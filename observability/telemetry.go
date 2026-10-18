// Package observability owns scoped OpenTelemetry providers and contextual logs.
package observability

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"
)

var Propagator = propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})

type Providers struct {
	Traces  trace.TracerProvider
	Metrics metric.MeterProvider
	close   func(context.Context) error
}

func (p *Providers) Shutdown(ctx context.Context) error {
	if p.close != nil {
		return p.close(ctx)
	}
	return nil
}

// NewTelemetry creates application-scoped trace and metric providers without changing globals.
// Service identifies the application; c configures exporters and sampling.
// Export queues are bounded, batch export never blocks request handlers, and
// lifecycle shutdown uses the caller's budget. OTLP uses HTTP/protobuf URLs.
func NewTelemetry(ctx context.Context, c TelemetryConfig, service ServiceConfig, out io.Writer) (*Providers, error) {
	if err := ValidateTelemetry(c); err != nil {
		return nil, err
	}
	if c.Traces.Enabled && c.Traces.Exporter == "stdout" && out == nil {
		return nil, errors.New("telemetry: stdout trace output is required")
	}
	res := resource.NewSchemaless(attribute.String("service.name", service.Name), attribute.String("service.version", service.Version), attribute.String("deployment.environment.name", service.Environment))
	var exporter sdktrace.SpanExporter
	var err error
	if c.Traces.Enabled {
		switch c.Traces.Exporter {
		case "stdout":
			exporter, err = stdouttrace.New(stdouttrace.WithWriter(out))
		case "otlp":
			opts := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(c.Traces.Endpoint), otlptracehttp.WithTimeout(3 * time.Second)}
			if c.Traces.Insecure {
				opts = append(opts, otlptracehttp.WithInsecure())
			}
			exporter, err = otlptracehttp.New(ctx, opts...)
		}
	}
	if err != nil {
		return nil, errors.New("telemetry: cannot initialize trace exporter")
	}
	options := []sdktrace.TracerProviderOption{sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(c.Traces.SampleRatio)))}
	if exporter != nil {
		options = append(options, sdktrace.WithBatcher(exporter, sdktrace.WithMaxQueueSize(2048), sdktrace.WithMaxExportBatchSize(512), sdktrace.WithExportTimeout(3*time.Second)))
	}
	tp := sdktrace.NewTracerProvider(options...)
	mpopts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	if c.Metrics.Enabled {
		opts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpointURL(c.Metrics.Endpoint), otlpmetrichttp.WithTimeout(3 * time.Second)}
		if c.Metrics.Insecure {
			opts = append(opts, otlpmetrichttp.WithInsecure())
		}
		exp, e := otlpmetrichttp.New(ctx, opts...)
		if e != nil {
			_ = tp.Shutdown(ctx)
			return nil, errors.New("telemetry: cannot initialize metric exporter")
		}
		mpopts = append(mpopts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(30*time.Second), sdkmetric.WithTimeout(3*time.Second))))
	}
	mp := sdkmetric.NewMeterProvider(mpopts...)
	return &Providers{Traces: tp, Metrics: mp, close: func(ctx context.Context) error { return errors.Join(mp.Shutdown(ctx), tp.Shutdown(ctx)) }}, nil
}

// Handler enriches every context-aware business log without shared mutable state.
type Handler struct{ slog.Handler }

func (h Handler) Handle(ctx context.Context, r slog.Record) error {
	span := trace.SpanContextFromContext(ctx)
	if span.IsValid() {
		r = r.Clone()
		r.AddAttrs(slog.String("trace_id", span.TraceID().String()), slog.String("span_id", span.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}
func (h Handler) WithAttrs(a []slog.Attr) slog.Handler { return Handler{h.Handler.WithAttrs(a)} }
func (h Handler) WithGroup(g string) slog.Handler      { return Handler{h.Handler.WithGroup(g)} }

func (p *Providers) ClientOptions() []grpc.DialOption {
	return []grpc.DialOption{grpc.WithStatsHandler(otelgrpc.NewClientHandler(otelgrpc.WithTracerProvider(p.Traces), otelgrpc.WithMeterProvider(p.Metrics), otelgrpc.WithPropagators(Propagator)))}
}
func (p *Providers) ServerOption(allow ...func(string) bool) grpc.ServerOption {
	options := []otelgrpc.Option{otelgrpc.WithTracerProvider(p.Traces), otelgrpc.WithMeterProvider(p.Metrics), otelgrpc.WithPropagators(Propagator)}
	if len(allow) > 0 {
		options = append(options, otelgrpc.WithFilter(func(info *stats.RPCTagInfo) bool { return allow[0](info.FullMethodName) }))
	}
	return grpc.StatsHandler(otelgrpc.NewServerHandler(options...))
}

func (p *Providers) HTTPHandler(h http.Handler) http.Handler {
	return otelhttp.NewHandler(h, "koala.http", otelhttp.WithTracerProvider(p.Traces), otelhttp.WithMeterProvider(p.Metrics), otelhttp.WithPropagators(Propagator), otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method }))
}
func (p *Providers) HTTPClient() *http.Client {
	return &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport, otelhttp.WithTracerProvider(p.Traces), otelhttp.WithMeterProvider(p.Metrics), otelhttp.WithPropagators(Propagator))}
}
