// Package koala hosts protobuf APIs over gRPC and an optional HTTP gateway.
package koala

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"buf.build/go/protovalidate"
	"github.com/bingyuanng/koala/config"
	"github.com/bingyuanng/koala/observability"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Registration uses static generated callbacks; no runtime factory discovery.
type Registration struct {
	Descriptor protoreflect.ServiceDescriptor
	GRPC       func(grpc.ServiceRegistrar)
	Gateway    func(context.Context, *runtime.ServeMux, *grpc.ClientConn) error
}
type Hook func(context.Context, string) (context.Context, error)
type Options struct {
	Logger        *slog.Logger
	Authenticate  Hook
	Authorize     Hook
	Telemetry     *observability.Providers
	OpenAPI       []byte
	DocsAuthorize func(http.ResponseWriter, *http.Request) bool
	Closers       []func(context.Context) error
	// Listen can supply platform listeners and enables lifecycle failure tests.
	Listen func(string, string) (net.Listener, error)
}
type App struct {
	cfg                      config.Config
	options                  Options
	mu                       sync.Mutex
	started                  bool
	registrations            []Registration
	methods                  map[string]time.Duration
	ready                    chan struct{}
	healthy                  atomic.Bool
	grpcAddress, httpAddress string
	logger                   *slog.Logger
}

func New(c config.Config, o Options) (*App, error) {
	// Own a snapshot, including mutable aggregate fields.
	o.OpenAPI = append([]byte(nil), o.OpenAPI...)
	o.Closers = append([]func(context.Context) error(nil), o.Closers...)
	c.Requests.MethodTimeouts = cloneMap(c.Requests.MethodTimeouts)
	c.Server.HTTP.AllowedOrigins = append([]string(nil), c.Server.HTTP.AllowedOrigins...)
	if err := config.Validate(c); err != nil {
		return nil, err
	}
	logger := o.Logger
	if logger == nil {
		level := new(slog.LevelVar)
		_ = level.UnmarshalText([]byte(c.Logging.Level))
		logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	}
	handler := logger.Handler()
	if _, ok := handler.(observability.Handler); !ok {
		handler = observability.Handler{Handler: handler}
	}
	logger = slog.New(handler).With("app", c.App.Name, "version", c.App.Version, "environment", c.App.Environment)
	return &App{cfg: c, options: o, methods: map[string]time.Duration{}, ready: make(chan struct{}), logger: logger}, nil
}
func cloneMap(m map[string]string) map[string]string {
	n := map[string]string{}
	for k, v := range m {
		n[k] = v
	}
	return n
}
func (a *App) Logger() *slog.Logger   { return a.logger }
func (a *App) Ready() <-chan struct{} { return a.ready }
func (a *App) Addresses() (grpcAddress, httpAddress string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.grpcAddress, a.httpAddress
}
func (a *App) Register(r Registration) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return errors.New("koala: registration is closed")
	}
	if r.Descriptor == nil || r.GRPC == nil || r.Gateway == nil {
		return errors.New("koala: registration requires descriptor and both callbacks")
	}
	name := string(r.Descriptor.FullName())
	for _, existing := range a.registrations {
		if string(existing.Descriptor.FullName()) == name {
			return fmt.Errorf("koala: duplicate service %s", name)
		}
	}
	for i := 0; i < r.Descriptor.Methods().Len(); i++ {
		m := r.Descriptor.Methods().Get(i)
		if m.IsStreamingClient() || m.IsStreamingServer() {
			return fmt.Errorf("koala: streaming is unsupported: %s", m.FullName())
		}
	}
	a.registrations = append(a.registrations, r)
	return nil
}

func (a *App) Run(ctx context.Context) (result error) {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return errors.New("koala: application can only run once")
	}
	a.started = true
	a.mu.Unlock()
	telemetry := a.options.Telemetry
	shutdownBudget, _ := time.ParseDuration(a.cfg.Server.ShutdownTimeout)
	var shutdownContext context.Context
	var shutdownCancel context.CancelFunc
	startShutdown := func() context.Context {
		if shutdownContext == nil {
			shutdownContext, shutdownCancel = context.WithTimeout(context.Background(), shutdownBudget)
		}
		return shutdownContext
	}
	// Every acquired resource is covered, including failures during binding or registration.
	closers := append([]func(context.Context) error{}, a.options.Closers...)
	defer func() {
		shutdown := startShutdown()
		defer shutdownCancel()
		for i := len(closers) - 1; i >= 0; i-- {
			result = errors.Join(result, closers[i](shutdown))
		}
		if telemetry != nil {
			result = errors.Join(result, telemetry.Shutdown(shutdown))
		}
	}()
	for _, r := range a.registrations {
		for i := 0; i < r.Descriptor.Methods().Len(); i++ {
			m := r.Descriptor.Methods().Get(i)
			key := string(r.Descriptor.FullName()) + "/" + string(m.Name())
			budget, _ := time.ParseDuration(a.cfg.Requests.DefaultTimeout)
			if v, ok := a.cfg.Requests.MethodTimeouts[key]; ok {
				budget, _ = time.ParseDuration(v)
			}
			a.methods["/"+key] = budget
		}
	}
	for key := range a.cfg.Requests.MethodTimeouts {
		if _, ok := a.methods["/"+key]; !ok {
			return fmt.Errorf("koala: request timeout references unknown RPC %s", key)
		}
	}
	messages := []proto.Message{}
	for _, r := range a.registrations {
		for i := 0; i < r.Descriptor.Methods().Len(); i++ {
			messages = append(messages, dynamicpb.NewMessage(r.Descriptor.Methods().Get(i).Input()))
		}
	}
	validator, err := protovalidate.New(protovalidate.WithMessages(messages...))
	if err != nil {
		return errors.New("koala: invalid request validation rules")
	}
	if telemetry == nil {
		telemetry, err = observability.New(ctx, a.cfg, os.Stdout)
		if err != nil {
			return err
		}
	}
	if telemetry.Traces == nil || telemetry.Metrics == nil {
		return errors.New("koala: custom telemetry requires both trace and metric providers")
	}
	serverOptions := []grpc.ServerOption{telemetry.ServerOption(func(method string) bool {
		_, known := a.methods[method]
		return known || method == "/grpc.health.v1.Health/Check" || method == "/grpc.health.v1.Health/Watch"
	}), grpc.ChainUnaryInterceptor(a.interceptor(validator, telemetry))}
	dialCredentials := credentials.TransportCredentials(insecure.NewCredentials())
	if a.cfg.Server.GRPC.CertFile != "" {
		cert, e := tls.LoadX509KeyPair(a.cfg.Server.GRPC.CertFile, a.cfg.Server.GRPC.KeyFile)
		if e != nil {
			return errors.New("koala: cannot load TLS certificate")
		}
		serverOptions = append(serverOptions, grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})))
		roots, e := x509.SystemCertPool()
		if e != nil {
			roots = x509.NewCertPool()
		}
		if a.cfg.Server.GRPC.CAFile != "" {
			pem, e := os.ReadFile(a.cfg.Server.GRPC.CAFile)
			if e != nil || !roots.AppendCertsFromPEM(pem) {
				return errors.New("koala: cannot load gateway CA")
			}
		}
		name := a.cfg.Server.GRPC.ServerName
		if name == "" {
			name = "localhost"
		}
		dialCredentials = credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: name, MinVersion: tls.VersionTLS12})
	}
	server := grpc.NewServer(serverOptions...)
	defer server.Stop()
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)
	// Protect public registration callbacks from turning a setup error into a process panic.
	if err := registerServices(server, a.registrations); err != nil {
		return err
	}
	listen := a.options.Listen
	if listen == nil {
		listen = net.Listen
	}
	gl, err := listen("tcp", net.JoinHostPort(a.cfg.Server.GRPC.Host, strconv.Itoa(a.cfg.Server.GRPC.Port)))
	if err != nil {
		return fmt.Errorf("koala: bind gRPC: %w", err)
	}
	defer gl.Close()
	var hl net.Listener
	if a.cfg.Server.HTTP.Enabled {
		hl, err = listen("tcp", net.JoinHostPort(a.cfg.Server.HTTP.Host, strconv.Itoa(a.cfg.Server.HTTP.Port)))
		if err != nil {
			return fmt.Errorf("koala: bind HTTP: %w", err)
		}
		defer hl.Close()
	}
	a.mu.Lock()
	a.grpcAddress = gl.Addr().String()
	if hl != nil {
		a.httpAddress = hl.Addr().String()
	}
	a.mu.Unlock()
	failed := make(chan error, 2)
	go func() { failed <- server.Serve(gl) }()
	var httpServer *http.Server
	var conn *grpc.ClientConn
	if hl != nil {
		target := gl.Addr().String()
		host, port, _ := net.SplitHostPort(target)
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			host = "127.0.0.1"
		}
		target = net.JoinHostPort(host, port)
		opts := append(telemetry.ClientOptions(), grpc.WithTransportCredentials(dialCredentials), grpc.WithChainUnaryInterceptor(a.clientDeadline()))
		conn, err = grpc.NewClient(target, opts...)
		if err != nil {
			return fmt.Errorf("koala: gateway client: %w", err)
		}
		defer conn.Close()
		gateway := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(func(key string) (string, bool) {
			switch strings.ToLower(key) {
			case "x-request-id":
				return strings.ToLower(key), true
			}
			return "", false
		}))
		for _, r := range a.registrations {
			if err = registerGateway(ctx, r, gateway, conn); err != nil {
				return fmt.Errorf("koala: gateway registration: %w", err)
			}
		}
		// Verify the shared connection before readiness, including TLS failures.
		startup, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err = healthpb.NewHealthClient(conn).Check(startup, &healthpb.HealthCheckRequest{})
		cancel()
		if err != nil {
			return fmt.Errorf("koala: gateway connection: %w", err)
		}
		handler, err := a.httpHandler(gateway, telemetry)
		if err != nil {
			return err
		}
		write, _ := time.ParseDuration(a.cfg.Server.HTTP.WriteTimeout)
		httpServer = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, WriteTimeout: write}
		go func() { failed <- httpServer.Serve(hl) }()
	}
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	for _, r := range a.registrations {
		healthServer.SetServingStatus(string(r.Descriptor.FullName()), healthpb.HealthCheckResponse_SERVING)
	}
	a.healthy.Store(true)
	close(a.ready)
	a.logger.Info("application ready", "grpc", a.grpcAddress, "http", a.httpAddress)
	select {
	case <-ctx.Done():
	case err = <-failed:
		if err == nil {
			err = errors.New("listener stopped unexpectedly")
		}
		result = fmt.Errorf("koala: serving failed: %w", err)
	}
	a.healthy.Store(false)
	healthServer.Shutdown()
	shutdown := startShutdown()
	if httpServer != nil {
		if e := httpServer.Shutdown(shutdown); e != nil {
			_ = httpServer.Close()
			result = errors.Join(result, e)
		}
	}
	if conn != nil {
		_ = conn.Close()
	}
	drained := make(chan struct{})
	go func() { server.GracefulStop(); close(drained) }()
	select {
	case <-drained:
	case <-shutdown.Done():
		server.Stop()
		result = errors.Join(result, shutdown.Err())
	}
	a.logger.Info("application stopped")
	return result
}
func registerServices(s *grpc.Server, rs []Registration) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("koala: service registration failed")
		}
	}()
	for _, r := range rs {
		r.GRPC(s)
	}
	return nil
}

// HTTP annotation routes provide bounded operation names and the dispatch
// deadline before gateway decoding. Additional bindings share the same policy.
func (a *App) httpHandler(gateway http.Handler, p *observability.Providers) (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !a.healthy.Load() {
			http.Error(w, "not ready", 503)
			return
		}
		w.WriteHeader(200)
	})
	if a.cfg.Server.HTTP.Docs && len(a.options.OpenAPI) > 0 {
		protect := func(next http.HandlerFunc) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				if a.options.DocsAuthorize != nil && !a.options.DocsAuthorize(w, r) {
					return
				}
				next(w, r)
			}
		}
		mux.HandleFunc("GET /openapi.json", protect(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(a.options.OpenAPI)
		}))
		mux.HandleFunc("GET /docs", protect(serveDocs))
	}
	for _, reg := range a.registrations {
		for i := 0; i < reg.Descriptor.Methods().Len(); i++ {
			method := reg.Descriptor.Methods().Get(i)
			rpc := "/" + string(reg.Descriptor.FullName()) + "/" + string(method.Name())
			rule, _ := proto.GetExtension(method.Options(), annotations.E_Http).(*annotations.HttpRule)
			if rule == nil {
				continue
			}
			rules := append([]*annotations.HttpRule{rule}, rule.AdditionalBindings...)
			for _, binding := range rules {
				verb, path := httpBinding(binding)
				if verb == "" {
					return nil, fmt.Errorf("koala: unsupported HTTP binding for %s", rpc)
				}
				if strings.ContainsAny(path, "=*") {
					return nil, fmt.Errorf("koala: HTTP path templates with wildcards are unsupported: %s", rpc)
				}
				pattern := verb + " " + path
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ctx, cancel := a.deadline(r.Context(), rpc)
					defer cancel()
					r = r.WithContext(ctx)
					r.Body = http.MaxBytesReader(w, r.Body, a.cfg.Server.HTTP.MaxBodyBytes)
					gateway.ServeHTTP(w, r)
				})
				if err := handleRoute(mux, pattern, p.HTTPHandler(otelRoute(pattern, a.httpCompletion(pattern, handler)))); err != nil {
					return nil, err
				}
			}
		}
	}
	return a.corsAndRecovery(mux), nil
}
func handleRoute(m *http.ServeMux, p string, h http.Handler) (err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("koala: conflicting HTTP route %s", p)
		}
	}()
	m.Handle(p, h)
	return nil
}
func httpBinding(r *annotations.HttpRule) (string, string) {
	switch x := r.Pattern.(type) {
	case *annotations.HttpRule_Get:
		return "GET", x.Get
	case *annotations.HttpRule_Post:
		return "POST", x.Post
	case *annotations.HttpRule_Put:
		return "PUT", x.Put
	case *annotations.HttpRule_Delete:
		return "DELETE", x.Delete
	case *annotations.HttpRule_Patch:
		return "PATCH", x.Patch
	}
	return "", ""
}

func registerGateway(ctx context.Context, r Registration, m *runtime.ServeMux, c *grpc.ClientConn) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("koala: gateway registration failed")
		}
	}()
	return r.Gateway(ctx, m, c)
}
