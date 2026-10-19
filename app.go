// Package koala hosts protobuf APIs over gRPC and an optional HTTP gateway.
package koala

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bingyuanng/koala/observability"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/reflect/protoreflect"
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
	o.Requests.MethodTimeouts = maps.Clone(o.Requests.MethodTimeouts)
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
	return &App{
		server:       server,
		requests:     o.Requests,
		options:      o,
		methods:      map[string]time.Duration{},
		ready:        make(chan struct{}),
		logger:       logger,
		gatewayToken: rand.Text(),
	}, nil
}

func (a *App) Logger() *slog.Logger { return a.logger }

func (a *App) Ready() <-chan struct{} { return a.ready }

func (a *App) IsReady() bool { return a.healthy.Load() }

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
