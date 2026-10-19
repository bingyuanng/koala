package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/bingyuanng/koala/internal/generate"
	"github.com/bingyuanng/koala/internal/scaffold"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

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
		module, e := generate.ApplicationImportPath(root)
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
	module := f.String("module", name, "Go module path (defaults to the project name)")
	version := f.String("framework-version", "", "runtime release version (defaults to the installed release CLI version)")
	frameworkPath := new(string)
	if frameworkVersion() == "" {
		f.StringVar(frameworkPath, "framework-path", "", "local Koala checkout (development builds only)")
	}
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unexpected project arguments")
	}
	if *frameworkPath != "" {
		if *version != "" {
			return errors.New("choose either --framework-version or --framework-path")
		}
		path, err := filepath.Abs(*frameworkPath)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(path, "go.mod"))
		if err != nil {
			return fmt.Errorf("--framework-path must point to a Koala checkout: %w", err)
		}
		module, err := modfile.Parse("go.mod", data, nil)
		if err != nil || module.Module == nil || module.Module.Mod.Path != "github.com/bingyuanng/koala" {
			return errors.New("--framework-path must point to module github.com/bingyuanng/koala")
		}
		*frameworkPath = path
		// A local replacement needs a syntactically valid requirement, not a published version.
		*version = "v0.0.0"
	} else if *version == "" {
		*version = frameworkVersion()
		if *version == "" {
			return errors.New("development CLI requires --framework-version <release> or --framework-path <local-checkout>")
		}
	}
	files, e := scaffold.Render("project", scaffold.Data{ModulePath: *module, ProjectName: name, ServiceName: "UserService", FrameworkVersion: *version, FrameworkPath: *frameworkPath})
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
	cli := "koala"
	if strings.HasSuffix(os.Args[0], "/koala") {
		cli = os.Args[0]
	}
	fmt.Fprintf(out, "\nCreated %s\nModule: %s\n", name, *module)
	if *frameworkPath != "" {
		fmt.Fprintf(out, "Runtime: local checkout at %s\n", *frameworkPath)
	} else {
		fmt.Fprintf(out, "Runtime: github.com/bingyuanng/koala@%s\n", *version)
	}
	fmt.Fprintf(out, "\nSample health service:\n  proto/health/v1/health.proto\n  internal/service/health/service.go\n\nNext steps:\n  cd %s\n  mise trust\n  mise install\n  %s gen\n  %s start\n\nHealth check:\n  curl -i http://127.0.0.1:8080/healthz\n\n", name, cli, cli)
	return nil
}

func frameworkVersion() string {
	if semver.IsValid(Version) {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Path == "github.com/bingyuanng/koala" && info.Main.Replace == nil && semver.IsValid(info.Main.Version) && !strings.Contains(info.Main.Version, "+dirty") {
		// Go can stamp checkout builds with an inferred module version. Only
		// module-installed binaries may use build metadata as a release default.
		for _, setting := range info.Settings {
			if setting.Key == "vcs" {
				return ""
			}
		}
		return info.Main.Version
	}
	return ""
}
