package koala_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	koala "github.com/bingyuanng/koala"
	"github.com/bingyuanng/koala/observability"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func probeOptions() koala.Options {
	return koala.Options{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Telemetry: &observability.Providers{Traces: tracenoop.NewTracerProvider(), Metrics: metricnoop.NewMeterProvider()},
	}
}

func TestGatewayWithoutProbes(t *testing.T) {
	app, err := koala.New(testConfig().Server, withRequests(probeOptions()))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Register(registration()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	select {
	case <-app.Ready():
	case err := <-done:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("startup timeout")
	}
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("shutdown timeout")
		}
		if app.IsReady() {
			t.Error("ready after shutdown")
		}
	}()
	if err := app.HandleHTTP("GET /late", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})); err == nil {
		t.Fatal("late route accepted")
	}
	grpcAddr, httpAddr := app.Addresses()
	client := &http.Client{Timeout: time.Second}
	for path, want := range map[string]int{"/readyz": 404, "/livez": 404, "/v1/users/123": 200} {
		response, err := client.Get("http://" + httpAddr + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != want {
			t.Errorf("%s: got %d, want %d", path, response.StatusCode, want)
		}
	}
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	checkCtx, checkCancel := context.WithTimeout(ctx, time.Second)
	defer checkCancel()
	_, err = healthpb.NewHealthClient(conn).Check(checkCtx, &healthpb.HealthCheckRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("implicit gRPC health: %v", err)
	}
}

func TestExplicitGRPCHealthUsesApplicationAuth(t *testing.T) {
	_, conn, stop := start(t, testConfig(), koala.Options{
		Authenticate: func(ctx context.Context, method string) (context.Context, error) {
			return ctx, status.Error(codes.Unauthenticated, "credentials required")
		},
	})
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
}

func TestReadyHookFailureCleansUp(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "panic"}[panics], func(t *testing.T) {
			options := probeOptions()
			var order []string
			options.OnReady = func(context.Context) error {
				if panics {
					panic("private")
				}
				return errors.New("ready failed")
			}
			options.OnShutdown = func(ctx context.Context) error {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("shutdown budget missing")
				}
				order = append(order, "shutdown")
				return errors.New("shutdown failed")
			}
			app, err := koala.New(testConfig().Server, withRequests(options))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = app.Run(ctx)
			if err == nil || !strings.Contains(err.Error(), "ready") || !strings.Contains(err.Error(), "shutdown failed") {
				t.Fatal(err)
			}
			if strings.Join(order, ",") != "shutdown" {
				t.Fatal(order)
			}
			if app.IsReady() {
				t.Fatal("ready after failure")
			}
			select {
			case <-app.Ready():
				t.Fatal("readiness announced on failed hook")
			default:
			}
		})
	}
}
