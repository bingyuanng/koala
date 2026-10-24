package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/bingyuanng/koala/internal/generate"
	"github.com/bingyuanng/koala/internal/startup"
)

func generateCommand(name string, args []string, out, errout io.Writer) error {
	f := flags(name, errout)
	buf := f.String("buf", "", "pinned Buf executable")
	strict := f.Bool("strict", false, "reject inherited Unimplemented RPCs")
	gen := f.Bool("gen", false, "generate before starting")
	if e := f.Parse(args); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	cwd, e := os.Getwd()
	if e != nil {
		return e
	}
	root, e := generate.Find(cwd)
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if name != "start" || *gen {
		if e = generate.Run(ctx, root, generate.Options{Buf: *buf, Strict: *strict, Output: out}); e != nil {
			return e
		}
	}
	if name != "start" {
		return nil
	}
	if e = generate.CheckFresh(ctx, root); e != nil {
		return e
	}
	if e = generate.ReportFallbacks(ctx, root, out); e != nil {
		return e
	}
	dir, e := os.MkdirTemp("", "koala-start-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	out = &synchronizedWriter{writer: out}
	fmt.Fprintln(out, "Building application...")
	binary := filepath.Join(dir, "server")
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, "./cmd/server")
	build.Dir = root
	build.Stdout = out
	build.Stderr = errout
	if e = build.Run(); e != nil {
		return e
	}
	readiness, notify, e := os.Pipe()
	if e != nil {
		return e
	}
	defer readiness.Close()
	defer notify.Close()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	cmd := exec.Command(binary)
	cmd.Dir = root
	cmd.Stdout = out
	cmd.Stderr = errout
	cmd.Stdin = os.Stdin
	cmd.ExtraFiles = []*os.File{notify}
	cmd.Env = append(os.Environ(), startup.Environment+"=3")
	fmt.Fprintln(out, "Starting application...")
	if e = cmd.Start(); e != nil {
		return e
	}
	notify.Close()
	ready := make(chan startup.Notification, 1)
	go func() {
		var notification startup.Notification
		if json.NewDecoder(readiness).Decode(&notification) == nil {
			ready <- notification
		}
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case notification := <-ready:
			printStartup(out, notification)
			ready = nil
		case e = <-done:
			return e
		case forwarded := <-signals:
			// Forward the received signal to allow the configured runtime shutdown budget. A later
			// signal can still terminate the foreground process from the terminal.
			_ = cmd.Process.Signal(forwarded)
			return <-done
		}
	}
}
