package koala

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/bingyuanng/koala/observability"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
)

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
