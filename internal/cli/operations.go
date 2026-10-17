package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/bingyuanng/koala/config"
	"github.com/bingyuanng/koala/internal/generate"
	"github.com/bingyuanng/koala/internal/scaffold"
)

func flags(name string, out io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(out)
	return f
}
func operation(args []string, out, errout io.Writer) error {
	switch args[0] {
	case "new":
		return newCommand(args[1:], out, errout)
	case "config":
		return configCommand(args[1:], out, errout)
	case "gen", "generate", "start":
		f := flags(args[0], errout)
		buf := f.String("buf", "", "pinned Buf executable")
		localPlugins := f.String("local-plugins", "", "explicit directory of pinned local protoc plugins")
		strict := f.Bool("strict", false, "reject inherited Unimplemented RPCs")
		gen := f.Bool("gen", false, "generate before starting")
		if e := f.Parse(args[1:]); e != nil {
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
		if args[0] != "start" || *gen {
			if e = generate.Run(ctx, root, generate.Options{Buf: *buf, LocalPlugins: *localPlugins, Strict: *strict, Output: out}); e != nil {
				return e
			}
		}
		if args[0] != "start" {
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
		binary := filepath.Join(dir, "server")
		build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, "./cmd/server")
		build.Dir = root
		build.Stdout = out
		build.Stderr = errout
		if e = build.Run(); e != nil {
			return e
		}
		signals := make(chan os.Signal, 2)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(signals)
		cmd := exec.Command(binary)
		cmd.Dir = root
		cmd.Stdout = out
		cmd.Stderr = errout
		cmd.Stdin = os.Stdin
		if e = cmd.Start(); e != nil {
			return e
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case e = <-done:
			return e
		case forwarded := <-signals:
			// Forward the received signal to allow the configured runtime shutdown budget. A later
			// signal can still terminate the foreground process from the terminal.
			_ = cmd.Process.Signal(forwarded)
			return <-done
		}
	}
	return errors.New("unsupported operation")
}
func newCommand(args []string, out, errout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: koala new <project> [--module path] | koala new service <name>")
	}
	if args[0] == "service" {
		if len(args) != 2 {
			return errors.New("usage: koala new service <name>")
		}
		root, e := generate.Find(".")
		if e != nil {
			return e
		}
		module, e := readModule(root)
		if e != nil {
			return e
		}
		name := args[1]
		if !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`).MatchString(name) {
			return errors.New("service name must be an identifier")
		}
		name = strings.ToUpper(name[:1]) + name[1:]
		if !strings.HasSuffix(name, "Service") {
			name += "Service"
		}
		files, e := scaffold.Render("service", scaffold.Data{ModulePath: module, ProjectName: filepath.Base(root), ServiceName: name, FrameworkVersion: frameworkVersion()})
		if e != nil {
			return e
		}
		if e = writeFiles(root, files); e != nil {
			return e
		}
		fmt.Fprintln(out, "Created", name, "application-owned contract and shell; run koala gen")
		return nil
	}
	name := args[0]
	if !regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`).MatchString(name) {
		return errors.New("project name must be a single directory name")
	}
	f := flags("new", errout)
	module := f.String("module", "example.com/"+name, "Go module path")
	version := f.String("framework-version", frameworkVersion(), "runtime version")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unexpected project arguments")
	}
	files, e := scaffold.Render("project", scaffold.Data{ModulePath: *module, ProjectName: name, ServiceName: "UserService", FrameworkVersion: *version})
	if e != nil {
		return e
	}
	if e = os.Mkdir(name, 0755); e != nil {
		return fmt.Errorf("refusing existing project directory: %w", e)
	}
	if e = writeFiles(name, files); e != nil {
		_ = os.RemoveAll(name)
		return e
	}
	fmt.Fprintln(out, "Created", name, "with module", *module)
	return nil
}
func frameworkVersion() string {
	if strings.HasPrefix(Version, "v") {
		return Version
	}
	return "v0.1.0"
}
func readModule(root string) (string, error) { return generate.ApplicationImportPath(root) }

func writeFiles(root string, files []scaffold.File) error {
	// Preflight all outputs, including existing symlink ancestors, before writing.
	for _, file := range files {
		target := filepath.Join(root, file.Path)
		if _, e := os.Lstat(target); e == nil {
			return fmt.Errorf("refusing to overwrite %s", file.Path)
		} else if !os.IsNotExist(e) {
			return e
		}
		for d := filepath.Dir(target); d != filepath.Clean(root) && d != "." && d != "/"; d = filepath.Dir(d) {
			if info, e := os.Lstat(d); e == nil && info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing symlink ancestor for %s", file.Path)
			}
		}
	}
	created := []string{}
	rollback := func() {
		for _, p := range created {
			_ = os.Remove(p)
		}
	}
	for _, file := range files {
		target := filepath.Join(root, file.Path)
		if e := os.MkdirAll(filepath.Dir(target), 0755); e != nil {
			rollback()
			return e
		}
		f, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if e != nil {
			rollback()
			return e
		}
		created = append(created, target)
		_, e = f.WriteString(file.Content)
		e = errors.Join(e, f.Close())
		if e != nil {
			rollback()
			return e
		}
	}
	return nil
}
func configCommand(args []string, out, errout io.Writer) error {
	if len(args) == 0 || (args[0] != "json" && args[0] != "env") {
		return errors.New("usage: koala config json|env [--output path] [--force]")
	}
	f := flags("config", errout)
	output := f.String("output", "", "output file")
	force := f.Bool("force", false, "replace output file")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unexpected config arguments")
	}
	c := config.DefaultConfig()
	var b []byte
	var e error
	if args[0] == "json" {
		b, e = json.MarshalIndent(c, "", "  ")
		b = append(b, '\n')
	} else {
		raw, _ := json.Marshal(c)
		var values map[string]any
		_ = json.Unmarshal(raw, &values)
		lines := []string{"# Framework defaults only. Applications own additional settings.", "KOALA_ENV=cloud"}
		flattenEnv("KOALA_", values, &lines)
		b = []byte(strings.Join(lines, "\n") + "\n")
	}
	if e != nil {
		return e
	}
	if *output == "" {
		_, e = out.Write(b)
		return e
	}
	if info, err := os.Lstat(*output); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing symlink output")
	}
	mode := os.O_CREATE | os.O_WRONLY | os.O_EXCL
	if *force {
		mode = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	fout, e := os.OpenFile(*output, mode, 0600)
	if e != nil {
		return e
	}
	_, e = fout.Write(b)
	return errors.Join(e, fout.Close())
}
func flattenEnv(prefix string, values map[string]any, lines *[]string) {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		key := prefix + strings.ToUpper(k)
		v := values[k]
		if m, ok := v.(map[string]any); ok && len(m) > 0 {
			flattenEnv(key+"__", m, lines)
			continue
		}
		if v == nil {
			v = map[string]any{}
		}
		s, ok := v.(string)
		if !ok {
			b, _ := json.Marshal(v)
			s = string(b)
		}
		*lines = append(*lines, key+"='"+strings.ReplaceAll(s, "'", `'\''`)+"'")
	}
}
