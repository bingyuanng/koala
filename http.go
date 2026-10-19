package koala

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bingyuanng/koala/observability"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (a *App) gatewayHandler(method string, gateway http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := a.deadline(r.Context(), method)
		defer cancel()
		deadline, _ := ctx.Deadline()
		if r.Body != nil && r.Body != http.NoBody {
			controller := http.NewResponseController(w)
			if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
				a.logger.ErrorContext(ctx, "cannot set request body deadline", "method", method, "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
		}
		// Leave the deadline in place through net/http's post-handler body drain.
		// The server resets it before reading the next request on this connection.
		r = r.WithContext(ctx)
		r.Body = http.MaxBytesReader(w, r.Body, a.server.HTTP.MaxBodyBytes)
		gateway.ServeHTTP(w, r)
	})
}

func gatewayErrorHandler(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
	// Generated decoders classify body read failures as InvalidArgument. Restore
	// deadline semantics even if the socket timeout fires before the context timer.
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		err = status.Error(codes.DeadlineExceeded, "context deadline exceeded")
	} else if ctx.Err() != nil {
		err = status.FromContextError(ctx.Err()).Err()
	}
	runtime.DefaultHTTPErrorHandler(ctx, mux, marshaler, w, r, err)
}

type httpRoute struct {
	pattern string
	handler http.Handler
}

// HandleHTTP explicitly registers an application-owned route before Run.
// Patterns use net/http.ServeMux syntax. HTTP-disabled apps do not serve them.
func (a *App) HandleHTTP(pattern string, handler http.Handler) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return errors.New("koala: HTTP registration is closed")
	}
	if handler == nil {
		return errors.New("koala: HTTP handler is required")
	}
	mux := http.NewServeMux()
	for _, route := range a.httpRoutes {
		if err := handleRoute(mux, route.pattern, route.handler); err != nil {
			return err
		}
	}
	if err := handleRoute(mux, pattern, handler); err != nil {
		return err
	}
	a.httpRoutes = append(a.httpRoutes, httpRoute{pattern: pattern, handler: handler})
	return nil
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
				handler := a.gatewayHandler(rpc, gateway)
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
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("koala: conflicting HTTP route %s: %w", p, panicError("HTTP route registration", recovered))
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
