package koala

import (
	"context"
	"errors"
	"time"
)

// Close invokes application resource closers in reverse acquisition order under
// one explicit shutdown timeout. The caller should defer it as soon
// as resources are acquired and join its error with any startup or Run error.
// Run drains transports but never closes application resources.
// Closers must respect their context; Close cannot interrupt a blocking callback.
func Close(shutdownTimeout string, closers ...func(context.Context) error) error {
	budget, err := time.ParseDuration(shutdownTimeout)
	if err != nil || budget <= 0 {
		return errors.New("koala: server.shutdown_timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	var result error
	for i := len(closers) - 1; i >= 0; i-- {
		result = errors.Join(result, invokeLifecycleHook("resource cleanup", ctx, closers[i]))
	}
	return errors.Join(result, ctx.Err())
}
