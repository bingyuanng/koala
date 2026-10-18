package cli

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/bingyuanng/koala/internal/startup"
)

type synchronizedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

func printStartup(out io.Writer, notification startup.Notification) {
	var message strings.Builder
	fmt.Fprintln(&message, "\nServer started")
	if notification.HTTP != "" {
		fmt.Fprintf(&message, "  HTTP:   http://%s\n", notification.HTTP)
	}
	fmt.Fprintf(&message, "  gRPC:   %s\n", notification.GRPC)
	if notification.HTTP != "" {
		if notification.HealthPath != "" {
			fmt.Fprintf(&message, "  Health: http://%s%s\n", notification.HTTP, notification.HealthPath)
		}
		if notification.Docs {
			fmt.Fprintf(&message, "  Docs:   http://%s/docs\n", notification.HTTP)
		}
	}
	fmt.Fprintln(&message, "\nPress Ctrl+C to stop.")
	fmt.Fprint(out, message.String())
}
