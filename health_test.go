package koala

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bingyuanng/koala/observability"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func probeTestApp(t *testing.T) *App {
	t.Helper()
	app, err := New(ServerConfig{HTTP: HTTPConfig{Enabled: true, Docs: true, Port: 8080, WriteTimeout: "0s", MaxBodyBytes: 4 << 20}, GRPC: GRPCConfig{Port: 9090}, ShutdownTimeout: "15s"}, Options{Requests: RequestConfig{DefaultTimeout: "5s"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Telemetry: &observability.Providers{Traces: tracenoop.NewTracerProvider(), Metrics: metricnoop.NewMeterProvider()}})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestApplicationHTTPRoutes(t *testing.T) {
	app := probeTestApp(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusAccepted) })
	if err := app.HandleHTTP("GET /readyz", handler); err != nil {
		t.Fatal(err)
	}
	mux, err := app.httpHandler(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Code)
	}
	if err := app.HandleHTTP("GET /readyz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := app.HandleHTTP("GET /nil", nil); err == nil {
		t.Fatal("nil accepted")
	}
	if err := app.HandleHTTP("GET /{", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})); err == nil {
		t.Fatal("invalid pattern accepted")
	}
}

func TestApplicationRouteConflictsWithDocs(t *testing.T) {
	for _, path := range []string{"/docs", "/openapi.json"} {
		t.Run(path, func(t *testing.T) {
			app := probeTestApp(t)
			app.options.OpenAPI = []byte(`{"swagger":"2.0"}`)
			if err := app.HandleHTTP("GET "+path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})); err != nil {
				t.Fatal(err)
			}
			if _, err := app.httpHandler(nil, nil); err == nil {
				t.Fatal("conflicting documentation route accepted")
			}
		})
	}
}

func TestHTTPPanicDiagnostics(t *testing.T) {
	for _, completion := range []bool{false, true} {
		name := "application route"
		if completion {
			name = "completion wrapper"
		}
		t.Run(name, func(t *testing.T) {
			app := probeTestApp(t)
			var logs bytes.Buffer
			app.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			var handler http.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("private secret") })
			if completion {
				handler = app.httpCompletion("GET /panic", handler)
			} else {
				if err := app.HandleHTTP("GET /panic", handler); err != nil {
					t.Fatal(err)
				}
				var err error
				handler, err = app.httpHandler(nil, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", "/panic", nil))
			if response.Code != 500 || strings.Contains(response.Body.String(), "private secret") {
				t.Fatalf("panic response: %d %s", response.Code, response.Body.String())
			}
			if !strings.Contains(logs.String(), `"stack":`) || !strings.Contains(logs.String(), "TestHTTPPanicDiagnostics") || strings.Contains(logs.String(), "private secret") {
				t.Fatalf("missing diagnostics or exposed panic value: %s", logs.String())
			}
		})
	}
}
