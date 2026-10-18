package koala

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"buf.build/go/protovalidate"
	"github.com/bingyuanng/koala/observability"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Only this app's gateway can suppress the duplicate RPC completion record.
// A client-supplied flag must not disable direct gRPC completion logging.
const gatewayCompletionKey = "koala-gateway-completion"

func (a *App) deadline(ctx context.Context, method string) (context.Context, context.CancelFunc) {
	budget, ok := a.methods[method]
	if !ok {
		budget, _ = time.ParseDuration(a.requests.DefaultTimeout)
	}
	return context.WithTimeout(ctx, budget)
}
func (a *App) clientDeadline() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx, cancel := a.deadline(ctx, method)
		defer cancel()
		md, _ := metadata.FromOutgoingContext(ctx)
		md = md.Copy()
		md.Set(gatewayCompletionKey, a.gatewayToken)
		ctx = metadata.NewOutgoingContext(ctx, md)
		return invoke(ctx, method, req, reply, cc, opts...)
	}
}
func (a *App) interceptor(v protovalidate.Validator, p *observability.Providers) grpc.UnaryServerInterceptor {
	meter := p.Metrics.Meter("koala")
	invalid, _ := meter.Int64Counter("koala.rpc.validation_failures", metric.WithUnit("{failure}"))
	panics, _ := meter.Int64Counter("koala.rpc.panics", metric.WithUnit("{panic}"))
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (resp any, err error) {
		start := time.Now()
		md, _ := metadata.FromIncomingContext(ctx)
		values := md.Get(gatewayCompletionKey)
		gatewayRequest := len(values) == 1 && values[0] == a.gatewayToken
		if len(values) > 0 {
			md = md.Copy()
			md.Delete(gatewayCompletionKey)
			ctx = metadata.NewIncomingContext(ctx, md)
		}
		ctx, cancel := a.deadline(ctx, info.FullMethod)
		requestContext := ctx
		defer cancel()
		defer func() {
			if recover() != nil {
				panics.Add(ctx, 1, metric.WithAttributes(attribute.String("rpc.method", info.FullMethod)))
				err = status.Error(codes.Internal, "internal server error")
			}
			if requestContext.Err() != nil && err == nil {
				err = status.FromContextError(requestContext.Err()).Err()
			}
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
					err = status.FromContextError(err).Err()
				} else if _, ok := status.FromError(err); !ok {
					a.logger.ErrorContext(ctx, "application request failed", "method", info.FullMethod)
					err = status.Error(codes.Internal, "internal server error")
				}
			}
			if !gatewayRequest {
				a.logger.InfoContext(ctx, "RPC completed", "transport", "grpc", "method", info.FullMethod, "status", status.Code(err).String(), "duration_ms", float64(time.Since(start).Microseconds())/1000)
			}
		}()
		for _, hook := range []Hook{a.options.Authenticate, a.options.Authorize} {
			if hook != nil {
				nextContext, hookError := hook(ctx, info.FullMethod)
				if hookError != nil {
					return nil, hookError
				}
				if nextContext == nil {
					return nil, status.Error(codes.Internal, "invalid application hook context")
				}
				// Hooks may attach values, but cannot extend or detach the
				// request budget or upstream cancellation.
				parent := ctx
				deadline, _ := parent.Deadline()
				bounded, release := context.WithDeadline(nextContext, deadline)
				stop := context.AfterFunc(parent, func() {
					if parent.Err() != context.DeadlineExceeded {
						release()
					}
				})
				defer func() { stop(); release() }()
				ctx = bounded
				if !trace.SpanContextFromContext(ctx).IsValid() {
					ctx = trace.ContextWithSpan(ctx, trace.SpanFromContext(parent))
				}
			}
		}
		if msg, ok := req.(proto.Message); ok {
			if e := v.Validate(msg); e != nil {
				var violations *protovalidate.ValidationError
				if !errors.As(e, &violations) {
					return nil, status.Error(codes.Internal, "request validation unavailable")
				}
				invalid.Add(ctx, 1, metric.WithAttributes(attribute.String("rpc.method", info.FullMethod)))
				details := &errdetails.BadRequest{}
				for _, violation := range violations.Violations {
					field := protovalidate.FieldPathString(violation.Proto.GetField())

					details.FieldViolations = append(details.FieldViolations, &errdetails.BadRequest_FieldViolation{Field: field, Description: "constraint violated: " + violation.Proto.GetRuleId()})
				}
				result, _ := status.New(codes.InvalidArgument, "request validation failed").WithDetails(details)
				return nil, result.Err()
			}
		}
		return next(ctx, req)
	}
}
func otelRoute(pattern string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		span := trace.SpanFromContext(r.Context())
		span.SetName(pattern)
		span.SetAttributes(attribute.String("http.route", pattern))
		labeler, _ := otelhttp.LabelerFromContext(r.Context())
		labeler.Add(attribute.String("http.route", pattern))
		h.ServeHTTP(w, r)
	})
}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (w *responseRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
		w.ResponseWriter.WriteHeader(code)
	}
}
func (w *responseRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (w *responseRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (a *App) httpCompletion(pattern string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w}
		if id := r.Header.Get("X-Request-Id"); id != "" {
			w.Header().Set("X-Request-Id", id)
		}
		sc := trace.SpanContextFromContext(r.Context())
		if sc.IsValid() {
			w.Header().Set("X-Trace-Id", sc.TraceID().String())
		}
		defer func() {
			if recover() != nil {
				http.Error(rec, "internal server error", 500)
			}
			code := rec.status
			if code == 0 {
				code = 200
			}
			a.logger.InfoContext(r.Context(), "HTTP completed", "transport", "http", "route", pattern, "status", code, "duration_ms", float64(time.Since(start).Microseconds())/1000)
		}()
		next.ServeHTTP(rec, r)
	})
}
func (a *App) corsAndRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				http.Error(w, "internal server error", 500)
			}
		}()
		origin := r.Header.Get("Origin")
		if origin != "" {
			allowed := false
			for _, o := range a.server.HTTP.AllowedOrigins {
				if o == origin {
					allowed = true
				}
			}
			if !allowed {
				http.Error(w, "origin not allowed", 403)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Traceparent, Tracestate")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			if r.Method == http.MethodOptions {
				w.WriteHeader(204)
				return
			}
		}
		// Request IDs are independent of trace IDs. Only bounded printable IDs
		// are forwarded; malformed input is dropped rather than echoed.
		if id := r.Header.Get("X-Request-Id"); id != "" && !requestIDPattern.MatchString(id) {
			r.Header.Del("X-Request-Id")
		}
		r.Header.Del("X-Forwarded-For")
		r.Header.Del("X-Forwarded-Host")
		r.Header.Del("X-Forwarded-Proto")
		// Clients cannot inject arbitrary internal gRPC metadata through HTTP headers.
		for key := range r.Header {
			if strings.HasPrefix(strings.ToLower(key), "grpc-metadata-") {
				r.Header.Del(key)
			}
		}
		next.ServeHTTP(w, r)
	})
}

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
