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
)

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
	validator, err := a.prepareRequests()
	if err != nil {
		return err
	}
	serverOptions := []grpc.ServerOption{telemetry.ServerOption(func(method string) bool {
		_, known := a.methods[method]
		return known
	}), grpc.ChainUnaryInterceptor(a.interceptor(validator, telemetry))}
	tlsOption, dialCredentials, err := a.transportCredentials()
	if err != nil {
		return err
	}
	if tlsOption != nil {
		serverOptions = append(serverOptions, tlsOption)
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
		opts := append(telemetry.ClientOptions(), grpc.WithTransportCredentials(dialCredentials), grpc.WithChainUnaryInterceptor(a.gatewayInterceptor()))
		conn, err = grpc.NewClient(target, opts...)
		if err != nil {
			return fmt.Errorf("koala: gateway client: %w", err)
		}
		defer conn.Close()
		httpServer, err = a.gatewayServer(ctx, conn)
		if err != nil {
			return err
		}
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
	result = errors.Join(result, drainTransports(shutdown, httpServer, conn, server))
	a.logger.Info("application stopped")
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

func drainTransports(shutdown context.Context, httpServer *http.Server, conn *grpc.ClientConn, server *grpc.Server) error {
	var result error
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
	return result
}
