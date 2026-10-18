// Package koala hosts protobuf APIs over gRPC and an optional HTTP gateway.
package koala

import (
	"context"
	"crypto/rand"
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
	"github.com/bingyuanng/koala/internal/startup"
	"github.com/bingyuanng/koala/observability"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
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
	// Requests configures shared gRPC/HTTP request budgets.
	Requests RequestConfig
	// Logger is required and used unchanged. Applications configure handlers and attributes.
	Logger       *slog.Logger
	Authenticate Hook
	Authorize    Hook
	// Telemetry is required. The application retains ownership and cleanup responsibility.
	Telemetry     *observability.Providers
	OpenAPI       []byte
	DocsAuthorize func(http.ResponseWriter, *http.Request) bool
	// RegisterGRPC explicitly installs additional application-selected gRPC services.
	RegisterGRPC func(*grpc.Server) error
	// OnReady runs before readiness is announced. OnShutdown runs before draining
	// or when startup fails, within the same budget as transport draining.
	OnReady    func(context.Context) error
	OnShutdown func(context.Context) error
	// Listen can supply platform listeners and enables lifecycle failure tests.
	Listen func(string, string) (net.Listener, error)
}
type App struct {
	server                   ServerConfig
	requests                 RequestConfig
	options                  Options
	mu                       sync.Mutex
	started                  bool
	registrations            []Registration
	httpRoutes               []httpRoute
	methods                  map[string]time.Duration
	ready                    chan struct{}
	healthy                  atomic.Bool
	grpcAddress, httpAddress string
	logger                   *slog.Logger
	gatewayToken             string
}

// New constructs the transport runtime from server settings and explicit options.
// Options.Requests supplies request budgets. Application configuration and resource
// construction remain with the caller.
func New(server ServerConfig, o Options) (*App, error) {
	// Own a snapshot, including mutable aggregate fields.
	o.OpenAPI = append([]byte(nil), o.OpenAPI...)
	o.Requests.MethodTimeouts = cloneMap(o.Requests.MethodTimeouts)
	server.HTTP.AllowedOrigins = append([]string(nil), server.HTTP.AllowedOrigins...)
	if err := ValidateServerConfig(server, o.Requests); err != nil {
		return nil, err
	}
	logger := o.Logger
	if logger == nil {
		return nil, errors.New("koala: Logger is required; construct and pass an application logger")
	}
	if o.Telemetry == nil || o.Telemetry.Traces == nil || o.Telemetry.Metrics == nil {
		return nil, errors.New("koala: Telemetry with trace and metric providers is required; construct and pass providers explicitly")
	}
	return &App{server: server, requests: o.Requests, options: o, methods: map[string]time.Duration{}, ready: make(chan struct{}), logger: logger, gatewayToken: rand.Text()}, nil
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
func (a *App) IsReady() bool          { return a.healthy.Load() }
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
	shutdownBudget, _ := time.ParseDuration(a.server.ShutdownTimeout)
	var shutdownContext context.Context
	var shutdownCancel context.CancelFunc
	startShutdown := func() context.Context {
		if shutdownContext == nil {
			shutdownContext, shutdownCancel = context.WithTimeout(context.Background(), shutdownBudget)
		}
		return shutdownContext
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		a.healthy.Store(false)
		result = errors.Join(result, invokeLifecycleHook("shutdown", startShutdown(), a.options.OnShutdown))
	}
	defer func() {
		stop()
		if shutdownCancel != nil {
			shutdownCancel()
		}
	}()
	for _, r := range a.registrations {
		for i := 0; i < r.Descriptor.Methods().Len(); i++ {
			m := r.Descriptor.Methods().Get(i)
			key := string(r.Descriptor.FullName()) + "/" + string(m.Name())
			budget, _ := time.ParseDuration(a.requests.DefaultTimeout)
			if v, ok := a.requests.MethodTimeouts[key]; ok {
				budget, _ = time.ParseDuration(v)
			}
			a.methods["/"+key] = budget
		}
	}
	for key := range a.requests.MethodTimeouts {
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
	serverOptions := []grpc.ServerOption{telemetry.ServerOption(func(method string) bool {
		_, known := a.methods[method]
		return known
	}), grpc.ChainUnaryInterceptor(a.interceptor(validator, telemetry))}
	dialCredentials := credentials.TransportCredentials(insecure.NewCredentials())
	if a.server.GRPC.CertFile != "" {
		cert, e := tls.LoadX509KeyPair(a.server.GRPC.CertFile, a.server.GRPC.KeyFile)
		if e != nil {
			return errors.New("koala: cannot load TLS certificate")
		}
		serverOptions = append(serverOptions, grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})))
		roots, e := x509.SystemCertPool()
		if e != nil {
			roots = x509.NewCertPool()
		}
		if a.server.GRPC.CAFile != "" {
			pem, e := os.ReadFile(a.server.GRPC.CAFile)
			if e != nil || !roots.AppendCertsFromPEM(pem) {
				return errors.New("koala: cannot load gateway CA")
			}
		}
		name := a.server.GRPC.ServerName
		if name == "" {
			name = "localhost"
		}
		dialCredentials = credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: name, MinVersion: tls.VersionTLS12})
	}
	server := grpc.NewServer(serverOptions...)
	defer server.Stop()
	// Protect public registration callbacks from turning a setup error into a process panic.
	if err := registerServices(server, a.registrations, a.options.RegisterGRPC); err != nil {
		return err
	}
	budget, _ := time.ParseDuration(a.requests.DefaultTimeout)
	for service, info := range server.GetServiceInfo() {
		for _, method := range info.Methods {
			name := "/" + service + "/" + method.Name
			if _, known := a.methods[name]; !known {
				a.methods[name] = budget
			}
		}
	}
	listen := a.options.Listen
	if listen == nil {
		listen = net.Listen
	}
	gl, err := listen("tcp", net.JoinHostPort(a.server.GRPC.Host, strconv.Itoa(a.server.GRPC.Port)))
	if err != nil {
		return fmt.Errorf("koala: bind gRPC: %w", err)
	}
	defer gl.Close()
	var hl net.Listener
	if a.server.HTTP.Enabled {
		hl, err = listen("tcp", net.JoinHostPort(a.server.HTTP.Host, strconv.Itoa(a.server.HTTP.Port)))
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
		conn.Connect()
		for state := conn.GetState(); state != connectivity.Ready; state = conn.GetState() {
			if !conn.WaitForStateChange(startup, state) {
				err = startup.Err()
				break
			}
		}
		cancel()
		if err != nil {
			return fmt.Errorf("koala: gateway connection: %w", err)
		}
		handler, err := a.httpHandler(gateway, telemetry)
		if err != nil {
			return err
		}
		write, _ := time.ParseDuration(a.server.HTTP.WriteTimeout)
		httpServer = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, WriteTimeout: write}
		go func() { failed <- httpServer.Serve(hl) }()
	}
	if err := invokeLifecycleHook("ready", ctx, a.options.OnReady); err != nil {
		return err
	}
	a.healthy.Store(true)
	close(a.ready)
	healthPath := ""
	if a.hasHTTPRoute("GET", "/healthz") {
		healthPath = "/healthz"
	} else if a.hasHTTPRoute("GET", "/readyz") {
		healthPath = "/readyz"
	}
	if err := startup.Notify(startup.Notification{GRPC: a.grpcAddress, HTTP: a.httpAddress, HealthPath: healthPath, Docs: a.server.HTTP.Docs && len(a.options.OpenAPI) > 0}); err != nil {
		a.logger.Warn("startup notification failed", "error", err)
	}
	a.logger.Info("application ready", "grpc", a.grpcAddress, "http", a.httpAddress)
	select {
	case <-ctx.Done():
	case err = <-failed:
		if err == nil {
			err = errors.New("listener stopped unexpectedly")
		}
		result = fmt.Errorf("koala: serving failed: %w", err)
	}
	stop()
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
func registerServices(s *grpc.Server, rs []Registration, extra func(*grpc.Server) error) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("koala: service registration failed")
		}
	}()
	for _, r := range rs {
		r.GRPC(s)
	}
	if extra != nil {
		return extra(s)
	}
	return nil
}

func invokeLifecycleHook(stage string, ctx context.Context, hook func(context.Context) error) (err error) {
	if hook == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("koala: %s hook panicked", stage)
		}
	}()
	return hook(ctx)
}

// HTTP annotation routes provide bounded operation names and the dispatch
// deadline before gateway decoding. Additional bindings share the same policy.
func (a *App) httpHandler(gateway http.Handler, p *observability.Providers) (http.Handler, error) {
	mux := http.NewServeMux()
	for _, route := range a.httpRoutes {
		if err := handleRoute(mux, route.pattern, route.handler); err != nil {
			return nil, err
		}
	}
	if a.server.HTTP.Docs && len(a.options.OpenAPI) > 0 {
		protect := func(next http.HandlerFunc) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				if a.options.DocsAuthorize != nil && !a.options.DocsAuthorize(w, r) {
					return
				}
				next(w, r)
			}
		}
		if err := handleRoute(mux, "GET /openapi.json", protect(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(a.options.OpenAPI)
		})); err != nil {
			return nil, err
		}
		if err := handleRoute(mux, "GET /docs", protect(serveDocs)); err != nil {
			return nil, err
		}
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
					r.Body = http.MaxBytesReader(w, r.Body, a.server.HTTP.MaxBodyBytes)
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

func (a *App) hasHTTPRoute(verb, path string) bool {
	for _, route := range a.httpRoutes {
		if route.pattern == verb+" "+path || route.pattern == path {
			return true
		}
	}
	for _, registration := range a.registrations {
		methods := registration.Descriptor.Methods()
		for i := 0; i < methods.Len(); i++ {
			rule, _ := proto.GetExtension(methods.Get(i).Options(), annotations.E_Http).(*annotations.HttpRule)
			if rule == nil {
				continue
			}
			for _, binding := range append([]*annotations.HttpRule{rule}, rule.AdditionalBindings...) {
				if method, route := httpBinding(binding); method == verb && route == path {
					return true
				}
			}
		}
	}
	return false
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
