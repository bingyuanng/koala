package koala

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	userv1 "github.com/bingyuanng/koala/internal/testdata/runtime/gen/go/user/v1"
	"github.com/bingyuanng/koala/observability"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
)

func probeTestApp(t *testing.T) *App {
	t.Helper()
	app, err := New(ServerConfig{HTTP: HTTPConfig{Enabled: true, Docs: true, Port: 8080, WriteTimeout: "0s", MaxBodyBytes: 4 << 20}, GRPC: GRPCConfig{Port: 9090}, ShutdownTimeout: "15s"}, Options{Requests: RequestConfig{DefaultTimeout: "5s"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Telemetry: &observability.Providers{Traces: tracenoop.NewTracerProvider(), Metrics: metricnoop.NewMeterProvider()}})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestProtobufRouteConflictsWithDocs(t *testing.T) {
	for _, path := range []string{"/docs", "/openapi.json"} {
		t.Run(path, func(t *testing.T) {
			app := probeTestApp(t)
			app.options.OpenAPI = []byte(`{"swagger":"2.0"}`)
			file := protodesc.ToFileDescriptorProto(userv1.File_user_v1_user_proto)
			proto.SetExtension(file.Service[0].Method[1].Options, annotations.E_Http, &annotations.HttpRule{Pattern: &annotations.HttpRule_Get{Get: path}})
			descriptor, err := protodesc.NewFile(file, protoregistry.GlobalFiles)
			if err != nil {
				t.Fatal(err)
			}
			app.registrations = []Registration{{Descriptor: descriptor.Services().Get(0)}}
			if _, err := app.httpHandler(http.NotFoundHandler(), app.options.Telemetry); err == nil {
				t.Fatal("conflicting documentation route accepted")
			}
		})
	}
}

func TestHTTPPanicDiagnostics(t *testing.T) {
	for _, completion := range []bool{false, true} {
		name := "outer recovery"
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
				handler = app.corsAndRecovery(handler)
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
