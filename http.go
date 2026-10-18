package koala

import (
	"errors"
	"net/http"
)

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
