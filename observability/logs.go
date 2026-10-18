package observability

import (
	"errors"
	"io"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/log"
)

// NewLogger constructs a JSON logger with the configured level and context trace
// IDs. Applications attach their own attributes with With. It writes only to
// output and does not change slog's global logger.
func NewLogger(cfg LoggerConfig, output io.Writer) (*slog.Logger, error) {
	if output == nil {
		return nil, errors.New("observability: logger output is required")
	}
	if err := ValidateLogger(cfg); err != nil {
		return nil, err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		return nil, errors.New("observability: invalid logging level")
	}
	logger := slog.New(Handler{
		Handler: slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level}),
	})
	return logger, nil
}

// LogHandler is the optional OpenTelemetry slog bridge. Supply it through
// koala.Options.Logger. The application owns shutdown of the supplied log provider.
// This replaces the default stdout handler, avoiding implicit duplicate export.
func LogHandler(provider log.LoggerProvider) slog.Handler {
	return Handler{Handler: otelslog.NewHandler("koala", otelslog.WithLoggerProvider(provider))}
}
