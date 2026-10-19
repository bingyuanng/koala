package koala_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
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
	grpcAddr, httpAddr := app.Addresses()
	client := &http.Client{Timeout: time.Second}
	for path, want := range map[string]int{"/healthz": 404, "/readyz": 404, "/livez": 404, "/v1/users/123": 200} {
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
			var app *koala.App
			var connection net.Conn
			request := func() error {
				if _, err := io.WriteString(connection, "GET /missing HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
					return err
				}
				response, err := http.ReadResponse(bufio.NewReader(connection), nil)
				if err != nil {
					return err
				}
				_, readError := io.Copy(io.Discard, response.Body)
				if err := errors.Join(readError, response.Body.Close()); err != nil {
					return err
				}
				if response.Close {
					return errors.New("server unexpectedly disabled keep-alive")
				}
				return nil
			}
			options.OnReady = func(context.Context) error {
				// Complete a request before failing startup so this is an accepted
				// keep-alive connection, not just a pending TCP connection.
				_, address := app.Addresses()
				var err error
				connection, err = net.DialTimeout("tcp", address, time.Second)
				if err != nil {
					return err
				}
				t.Cleanup(func() { connection.Close() })
				if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
					return err
				}
				if err := request(); err != nil {
					return err
				}
				if panics {
					panic("private")
				}
				return errors.New("ready failed")
			}
			options.OnShutdown = func(ctx context.Context) error {
				if app.IsReady() {
					t.Error("readiness must stop before shutdown hook")
				}
				// Startup failure uses the same hook-before-transports order as
				// normal shutdown. The accepted connection is still usable here.
				if connection != nil {
					if err := request(); err != nil {
						t.Errorf("HTTP closed before shutdown hook: %v", err)
					}
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Error("shutdown budget missing")
				}
				order = append(order, "shutdown")
				return errors.New("shutdown failed")
			}
			var err error
			app, err = koala.New(testConfig().Server, withRequests(options))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = app.Run(ctx)
			if err == nil || !strings.Contains(err.Error(), "ready") || !strings.Contains(err.Error(), "shutdown failed") {
				t.Fatal(err)
			}
			if panics && (!strings.Contains(err.Error(), "TestReadyHookFailureCleansUp") || strings.Contains(err.Error(), "private")) {
				t.Fatal("startup panic lost its stack or exposed its value", err)
			}
			if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			var buffer [1]byte
			_, connectionError := connection.Read(buffer[:])
			var networkError net.Error
			if connectionError == nil || (errors.As(connectionError, &networkError) && networkError.Timeout()) {
				t.Fatalf("accepted HTTP connection survived Run: %v", connectionError)
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
