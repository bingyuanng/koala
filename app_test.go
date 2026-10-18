package koala_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	koala "github.com/bingyuanng/koala"
	"github.com/bingyuanng/koala/config"
	userv1 "github.com/bingyuanng/koala/internal/testdata/runtime/gen/go/user/v1"
	"github.com/bingyuanng/koala/observability"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type service struct {
	userv1.UnimplementedUserServiceServer
}

func (s *service) CreateUser(ctx context.Context, r *userv1.CreateUserRequest) (*userv1.CreateUserResponse, error) {
	return &userv1.CreateUserResponse{User: &userv1.User{Id: "1", Name: r.Name, Email: r.Email}}, nil
}
func (s *service) GetUser(ctx context.Context, r *userv1.GetUserRequest) (*userv1.GetUserResponse, error) {
	switch r.Id {
	case "panic":
		panic("private secret")
	case "wait":
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &userv1.GetUserResponse{User: &userv1.User{Id: r.Id}}, nil
}
func registration() koala.Registration {
	return koala.Registration{Descriptor: userv1.File_user_v1_user_proto.Services().ByName("UserService"), GRPC: func(r grpc.ServiceRegistrar) { userv1.RegisterUserServiceServer(r, &service{}) }, Gateway: userv1.RegisterUserServiceHandler}
}
func testConfig() config.Config {
	c := config.DefaultConfig()
	c.Server.HTTP.Port = 0
	c.Server.GRPC.Port = 0
	c.Telemetry.Traces.Enabled = false
	c.Server.ShutdownTimeout = "1s"
	return c
}

// Tests explicitly construct the same resources that application main owns.
func newTestApp(t *testing.T, c config.Config, o koala.Options) (*koala.App, error) {
	t.Helper()
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	o.Logger = slog.New(observability.Handler{Handler: o.Logger.Handler()}).With("app", c.App.Name, "version", c.App.Version, "environment", c.App.Environment)
	if o.Telemetry == nil {
		p, err := observability.NewTelemetry(context.Background(), c.Telemetry, c.App, io.Discard)
		if err != nil {
			return nil, err
		}
		o.Telemetry = p
		var once sync.Once
		var shutdownError error
		shutdown := func(ctx context.Context) error {
			once.Do(func() { shutdownError = p.Shutdown(ctx) })
			return shutdownError
		}
		t.Cleanup(func() { _ = shutdown(context.Background()) })
	}
	grpcHealth := health.NewServer()
	grpcHealth.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	if o.RegisterGRPC == nil {
		o.RegisterGRPC = func(server *grpc.Server) error {
			healthpb.RegisterHealthServer(server, grpcHealth)
			for name := range server.GetServiceInfo() {
				grpcHealth.SetServingStatus(name, healthpb.HealthCheckResponse_NOT_SERVING)
			}
			return nil
		}
	}
	ready, shutdown := o.OnReady, o.OnShutdown
	o.OnReady = func(ctx context.Context) error {
		grpcHealth.Resume()
		if ready != nil {
			return ready(ctx)
		}
		return nil
	}
	o.OnShutdown = func(ctx context.Context) error {
		grpcHealth.Shutdown()
		if shutdown != nil {
			return shutdown(ctx)
		}
		return nil
	}
	o.Requests = c.Requests
	app, err := koala.New(c.Server, o)
	if err != nil {
		return nil, err
	}
	if err := app.HandleHTTP("GET /livez", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })); err != nil {
		return nil, err
	}
	if err := app.HandleHTTP("GET /readyz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !app.IsReady() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})); err != nil {
		return nil, err
	}
	return app, nil
}

func start(t *testing.T, c config.Config, o koala.Options) (*koala.App, *grpc.ClientConn, func()) {
	t.Helper()
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	a, e := newTestApp(t, c, o)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Register(registration()); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case <-a.Ready():
	case e := <-done:
		t.Fatal(e)
	case <-time.After(10 * time.Second):
		t.Fatal("startup timeout")
	}
	addr, _ := a.Addresses()
	conn, e := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if e != nil {
		t.Fatal(e)
	}
	return a, conn, func() {
		conn.Close()
		cancel()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(3 * time.Second):
			t.Error("shutdown timeout")
		}
	}
}
func TestTransportPoliciesAndDocs(t *testing.T) {
	c := testConfig()
	c.Requests.MethodTimeouts = map[string]string{"user.v1.UserService/GetUser": "20ms"}
	a, conn, stop := start(t, c, koala.Options{OpenAPI: []byte(`{"swagger":"2.0"}`)})
	defer stop()
	client := userv1.NewUserServiceClient(conn)
	if e := a.Register(registration()); e == nil {
		t.Fatal("late registration accepted")
	}
	if _, e := client.CreateUser(context.Background(), &userv1.CreateUserRequest{}); status.Code(e) != codes.InvalidArgument || len(status.Convert(e).Details()) == 0 {
		t.Fatalf("validation %v", e)
	}
	if _, e := client.GetUser(context.Background(), &userv1.GetUserRequest{Id: "panic"}); status.Code(e) != codes.Internal {
		t.Fatal(e)
	}
	if _, e := client.GetUser(context.Background(), &userv1.GetUserRequest{Id: "wait"}); status.Code(e) != codes.DeadlineExceeded {
		t.Fatal(e)
	}
	_, addr := a.Addresses()
	base := "http://" + addr
	for _, tt := range []struct {
		method, path, body string
		code               int
	}{{"POST", "/v1/users", `{"name":"Ada","email":"ada@example.com"}`, 200}, {"POST", "/v1/users", `{"name":""}`, 400}, {"GET", "/v1/users/1", "", 200}, {"GET", "/v1/users/wait", "", 504}, {"GET", "/v1/users/panic", "", 500}, {"GET", "/readyz", "", 200}, {"GET", "/openapi.json", "", 200}, {"GET", "/docs", "", 200}} {
		req, _ := http.NewRequest(tt.method, base+tt.path, strings.NewReader(tt.body))
		req.Header.Set("Content-Type", "application/json")
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tt.code {
			t.Fatalf("%s: %d %s", tt.path, resp.StatusCode, b)
		}
		if bytes.Contains(b, []byte("private secret")) {
			t.Fatal("panic leaked")
		}
	}
}
func TestGRPCOnlyAndRollback(t *testing.T) {
	c := testConfig()
	c.Server.HTTP.Enabled = false
	a, conn, stop := start(t, c, koala.Options{})
	defer stop()
	_, httpAddr := a.Addresses()
	if httpAddr != "" {
		t.Fatal("HTTP started")
	}
	if _, e := healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{}); e != nil {
		t.Fatal(e)
	}
	var first net.Listener
	calls := 0
	c = testConfig()
	a, e := newTestApp(t, c, koala.Options{Listen: func(network, addr string) (net.Listener, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("bind failure")
		}
		l, err := net.Listen(network, addr)
		first = l
		return l, err
	}})
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Run(context.Background()); e == nil {
		t.Fatal("expected bind failure")
	}
	if _, e = first.Accept(); e == nil {
		t.Fatal("listener leaked")
	}
}

type lockedBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}
func TestTraceParentsAndAuthParity(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	defer tp.Shutdown(context.Background())
	mp := sdkmetric.NewMeterProvider()
	defer mp.Shutdown(context.Background())
	var logs lockedBuffer
	hook := func(ctx context.Context, method string) (context.Context, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		if strings.Join(md.Get("authorization"), "") != "Bearer test" {
			return ctx, status.Error(codes.Unauthenticated, "authentication required")
		}
		return ctx, nil
	}
	a, conn, stop := start(t, testConfig(), koala.Options{Logger: slog.New(slog.NewJSONHandler(&logs, nil)), Authenticate: hook, Telemetry: &observability.Providers{Traces: tp, Metrics: mp}})
	defer stop()
	client := userv1.NewUserServiceClient(conn)
	if _, e := client.GetUser(context.Background(), &userv1.GetUserRequest{Id: "1"}); status.Code(e) != codes.Unauthenticated {
		t.Fatal(e)
	}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer test", "traceparent", "00-33333333333333333333333333333333-4444444444444444-01"))
	if _, e := client.GetUser(ctx, &userv1.GetUserRequest{Id: "1"}); e != nil {
		t.Fatal(e)
	}
	_, addr := a.Addresses()
	req, _ := http.NewRequest("GET", "http://"+addr+"/v1/users/1", nil)
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	spans := exp.GetSpans()
	directContinued := false
	for _, span := range spans {
		if span.SpanContext.TraceID().String() == "33333333333333333333333333333333" && span.Parent.SpanID().String() == "4444444444444444" {
			directContinued = true
		}
	}
	if !directContinued {
		t.Fatal("direct gRPC trace context was not continued")
	}

	parents := map[string]string{}
	for _, s := range spans {
		if s.SpanContext.TraceID().String() == "11111111111111111111111111111111" {
			parents[s.SpanContext.SpanID().String()] = s.Parent.SpanID().String()
		}
	}
	if len(parents) != 3 {
		t.Fatalf("expected connected HTTP/client/server spans, got %d", len(parents))
	}
	for _, parent := range parents {
		if parent != "2222222222222222" {
			if _, ok := parents[parent]; !ok {
				t.Fatal("disconnected span")
			}
		}
	}
	logs.Lock()
	s := logs.Buffer.String()
	logs.Unlock()
	if strings.Contains(s, "Bearer test") || !strings.Contains(s, `"trace_id":"11111111111111111111111111111111"`) {
		t.Fatalf("log correlation/privacy: %s", s)
	}
}
