package observability

import (
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/log"
)

// LogHandler is the optional OpenTelemetry slog bridge. Supply it through
// koala.Options.Logger and manage the supplied log provider in Options.Closers.
// This replaces the default stdout handler, avoiding implicit duplicate export.
func LogHandler(provider log.LoggerProvider) slog.Handler {
	return Handler{Handler: otelslog.NewHandler("koala", otelslog.WithLoggerProvider(provider))}
}
