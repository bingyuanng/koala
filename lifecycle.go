package koala

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/bingyuanng/koala/internal/startup"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// runningTransports records resources as startup acquires them, including partial
// startup. Run owns one cleanup path for all of these resources.
type runningTransports struct {
	grpc         *grpc.Server
	http         *http.Server
	gateway      *grpc.ClientConn
	grpcListener net.Listener
	httpListener net.Listener
	failed       chan error
}

func (a *App) Run(ctx context.Context) (result error) {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return errors.New("koala: application can only run once")
	}
	a.started = true
	a.mu.Unlock()

	transports := &runningTransports{failed: make(chan error, 2)}
	ready := false
	defer func() { result = errors.Join(result, a.shutdownTransports(transports, ready)) }()
	if err := a.startTransports(ctx, transports); err != nil {
		return err
	}
	if err := invokeLifecycleHook("ready", ctx, a.options.OnReady); err != nil {
		return err
	}
	ready = true
	a.healthy.Store(true)
	close(a.ready)
	a.announceReady()
	select {
	case <-ctx.Done():
		return nil
	case err := <-transports.failed:
		if err == nil {
			err = errors.New("listener stopped unexpectedly")
		}
		return fmt.Errorf("koala: serving failed: %w", err)
	}
}

// gRPC must serve during startup for the gateway connectivity/TLS preflight.
// Readiness is announced by Run only after both transports and OnReady succeed.
func (a *App) startTransports(ctx context.Context, transports *runningTransports) error {
	telemetry := a.options.Telemetry
	services := make([]protoreflect.ServiceDescriptor, len(a.registrations))
	for i, r := range a.registrations {
		services[i] = r.Descriptor
	}
	methods, validator, err := prepareRequests(a.requests, services)
	if err != nil {
		return err
	}
	a.methods = methods
	serverOptions := []grpc.ServerOption{telemetry.ServerOption(func(method string) bool {
		_, known := a.methods[method]
		return known
	}), grpc.ChainUnaryInterceptor(a.interceptor(validator, telemetry))}
	serverCredentials, dialCredentials, err := a.transportCredentials()
	if err != nil {
		return err
	}
	if serverCredentials != nil {
		serverOptions = append(serverOptions, serverCredentials)
	}
	server := grpc.NewServer(serverOptions...)
	transports.grpc = server
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
	grpcListener, err := listen("tcp", net.JoinHostPort(a.server.GRPC.Host, strconv.Itoa(a.server.GRPC.Port)))
	if err != nil {
		return fmt.Errorf("koala: bind gRPC: %w", err)
	}
	transports.grpcListener = grpcListener
	var httpListener net.Listener
	if a.server.HTTP.Enabled {
		httpListener, err = listen("tcp", net.JoinHostPort(a.server.HTTP.Host, strconv.Itoa(a.server.HTTP.Port)))
		if err != nil {
			return fmt.Errorf("koala: bind HTTP: %w", err)
		}
		transports.httpListener = httpListener
	}
	a.mu.Lock()
	a.grpcAddress = grpcListener.Addr().String()
	if httpListener != nil {
		a.httpAddress = httpListener.Addr().String()
	}
	a.mu.Unlock()
	go func() { transports.failed <- server.Serve(grpcListener) }()
	if httpListener != nil {
		target := grpcListener.Addr().String()
		host, port, _ := net.SplitHostPort(target)
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			host = "127.0.0.1"
		}
		target = net.JoinHostPort(host, port)
		clientOptions := append(telemetry.ClientOptions(), grpc.WithTransportCredentials(dialCredentials), grpc.WithChainUnaryInterceptor(a.gatewayInterceptor()))
		gatewayConnection, err := grpc.NewClient(target, clientOptions...)
		if err != nil {
			return fmt.Errorf("koala: gateway client: %w", err)
		}
		transports.gateway = gatewayConnection
		httpServer, err := a.gatewayServer(ctx, gatewayConnection)
		if err != nil {
			return err
		}
		transports.http = httpServer
		go func() { transports.failed <- httpServer.Serve(httpListener) }()
	}
	return nil
}

func (a *App) announceReady() {
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
}

// All exits use the same order: withdraw readiness and notify the application,
// finish HTTP, close its gateway connection, finish gRPC, release listeners.
// Failed startup closes immediately; a ready application drains within one budget.
func (a *App) shutdownTransports(t *runningTransports, graceful bool) error {
	a.healthy.Store(false)
	budget, _ := time.ParseDuration(a.server.ShutdownTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	result := invokeLifecycleHook("shutdown", ctx, a.options.OnShutdown)
	if t.http != nil {
		if graceful {
			result = errors.Join(result, t.http.Shutdown(ctx))
		}
		result = errors.Join(result, t.http.Close())
	}
	if t.gateway != nil {
		result = errors.Join(result, t.gateway.Close())
	}
	if t.grpc != nil {
		if graceful {
			drained := make(chan struct{})
			go func() { t.grpc.GracefulStop(); close(drained) }()
			select {
			case <-drained:
			case <-ctx.Done():
				t.grpc.Stop()
				result = errors.Join(result, ctx.Err())
			}
		} else {
			t.grpc.Stop()
		}
	}
	// A listener may have been bound before its server could start. Servers
	// close their own listeners too, so an already-closed listener is expected.
	for _, listener := range []net.Listener{t.httpListener, t.grpcListener} {
		if listener != nil {
			if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				result = errors.Join(result, err)
			}
		}
	}
	if graceful {
		a.logger.Info("application stopped")
	}
	return result
}

func registerServices(s *grpc.Server, rs []Registration, extra func(*grpc.Server) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError("service registration", recovered)
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
		if recovered := recover(); recovered != nil {
			err = panicError(stage+" hook", recovered)
		}
	}()
	return hook(ctx)
}

func registerGateway(ctx context.Context, r Registration, m *runtime.ServeMux, c *grpc.ClientConn) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError("gateway registration", recovered)
		}
	}()
	return r.Gateway(ctx, m, c)
}

func panicError(stage string, recovered any) error {
	return fmt.Errorf("koala: %s panicked (%T)\n%s", stage, recovered, debug.Stack())
}
