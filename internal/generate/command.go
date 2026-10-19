package generate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func command(ctx context.Context, root string, out io.Writer, name string, args ...string) error {
	for attempt := 0; ; attempt++ {
		var diagnostic bytes.Buffer
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = root
		cmd.Stdout = io.MultiWriter(out, &diagnostic)
		cmd.Stderr = cmd.Stdout
		e := cmd.Run()
		if e == nil {
			return nil
		}
		if attempt >= 3 || !strings.Contains(diagnostic.String(), "resource_exhausted") {
			return fmt.Errorf("%s failed: %w", name, e)
		}
		delay := time.Duration(10*(1<<attempt)) * time.Second
		fmt.Fprintf(out, "Buf rate limit; retrying in %s\n", delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

func bufBinary(explicit, version string) (string, error) {
	if explicit != "" {
		p, e := filepath.Abs(explicit)
		return p, e
	}
	p, e := exec.LookPath("buf")
	if e != nil {
		return "", fmt.Errorf("Buf %s is required; install it or pass --buf /path/to/buf", version)
	}
	return p, nil
}
