package koala

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
