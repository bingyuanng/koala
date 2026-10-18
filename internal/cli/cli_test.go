package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingMiseRejectsOperationsBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("PATH", dir)
	for _, args := range [][]string{
		{"new", "my-api"}, {"new", "service", "user"},
		{"gen"}, {"generate"}, {"config", "json", "--output", "config.json"}, {"start"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "mise is required on PATH") {
			t.Fatalf("%v: exit %d, stderr %s", args, code, &stderr)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("missing mise caused filesystem changes: %v, %v", entries, err)
	}
	for _, args := range [][]string{{"help"}, {"version"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("%v without mise: exit %d, stderr %s", args, code, &stderr)
		}
	}
}

func TestCommands(t *testing.T) {
	for _, tt := range []struct {
		args []string
		code int
		out  string
	}{
		{nil, 0, "Usage:"}, {[]string{"--help"}, 0, "Usage:"}, {[]string{"version"}, 0, "koala dev"},
		{[]string{"unknown"}, 2, ""}, {[]string{"help", "extra"}, 2, ""}, {[]string{"schema"}, 2, ""}, {[]string{"template", "project"}, 2, ""},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(tt.args, &stdout, &stderr); code != tt.code {
			t.Fatalf("%v: exit %d, stderr %s", tt.args, code, &stderr)
		}
		if !strings.Contains(stdout.String(), tt.out) {
			t.Fatalf("%v: stdout %s", tt.args, &stdout)
		}
		if tt.code != 0 && stderr.Len() == 0 {
			t.Fatal("error has no diagnostic")
		}
	}
}

func TestProjectRuntimeSelection(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	previous := Version
	Version = "dev"
	t.Cleanup(func() { Version = previous })
	for _, tt := range []struct {
		name  string
		flags []string
		want  string
	}{
		{"missing", nil, "development CLI requires"},
		{"both", []string{"--framework-version", "v0.2.0", "--framework-path", root}, "choose either"},
		{"invalid", []string{"--framework-path", t.TempDir()}, "must point to"},
	} {
		var out, stderr bytes.Buffer
		if code := Run(append([]string{"new", tt.name}, tt.flags...), &out, &stderr); code != 1 || !strings.Contains(stderr.String(), tt.want) {
			t.Fatalf("%s: exit %d, stderr %s", tt.name, code, &stderr)
		}
		if _, err := os.Stat(tt.name); !os.IsNotExist(err) {
			t.Fatalf("invalid runtime selection created %s", tt.name)
		}
	}
	for _, tt := range []struct {
		name  string
		flags []string
		want  string
	}{
		{"explicit", []string{"--framework-version", "v0.2.0"}, "require github.com/bingyuanng/koala v0.2.0"},
		{"local", []string{"--framework-path", root}, "replace github.com/bingyuanng/koala => "},
		{"release", nil, "require github.com/bingyuanng/koala v0.3.0"},
	} {
		if tt.name == "release" {
			Version = "v0.3.0"
		}
		var out, stderr bytes.Buffer
		if code := Run(append([]string{"new", tt.name}, tt.flags...), &out, &stderr); code != 0 {
			t.Fatalf("%s: exit %d, stderr %s", tt.name, code, &stderr)
		}
		module, err := os.ReadFile(filepath.Join(tt.name, "go.mod"))
		if err != nil || !strings.Contains(string(module), tt.want) {
			t.Fatalf("%s module: %s, %v", tt.name, module, err)
		}
		if tt.name == "local" && !strings.Contains(string(module), root) {
			t.Fatalf("local checkout absent from go.mod: %s", module)
		}
	}
}

func TestLocalFrameworkFlagIsDevelopmentOnly(t *testing.T) {
	previous := Version
	t.Cleanup(func() { Version = previous })
	t.Chdir(t.TempDir())
	for _, version := range []string{"dev", "v0.3.0"} {
		t.Run(version, func(t *testing.T) {
			Version = version
			var out, stderr bytes.Buffer
			if code := Run([]string{"help"}, &out, &stderr); code != 0 {
				t.Fatal(code, stderr.String())
			}
			if strings.Contains(out.String(), "--framework-path") != (version == "dev") {
				t.Fatal(out.String())
			}
			stderr.Reset()
			err := newCommand([]string{"unused", "--help"}, &out, &stderr)
			if err == nil {
				t.Fatal("expected help sentinel")
			}
			if strings.Contains(stderr.String(), "-framework-path") != (version == "dev") {
				t.Fatal(stderr.String())
			}
		})
	}
	Version = "v0.3.0"
	for _, flags := range [][]string{
		{"--framework-path", "."},
		{"--framework-path=."},
		{"--framework-version", "v0.2.0", "--framework-path", "."},
	} {
		var out, stderr bytes.Buffer
		if code := Run(append([]string{"new", "blocked"}, flags...), &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "flag provided but not defined: -framework-path") {
			t.Fatalf("release local override: exit %d, %s", code, stderr.String())
		}
		if _, err := os.Stat("blocked"); !os.IsNotExist(err) {
			t.Fatal("release rejection wrote a project")
		}
	}
	var out, stderr bytes.Buffer
	if code := Run([]string{"new", "override", "--framework-version", "v0.2.0"}, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	module, err := os.ReadFile("override/go.mod")
	if err != nil || !strings.Contains(string(module), "require github.com/bingyuanng/koala v0.2.0") || strings.Contains(string(module), "replace ") {
		t.Fatal(string(module), err)
	}
}

func TestConfigConsumerAndRelocatedCLI(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	binary := filepath.Join(outside, "koala")
	build := exec.Command("go", "build", "-o", binary, "./cmd/koala")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	// A checkout build may carry a Go-inferred pseudo-version. It must not
	// silently use that unpublished version when creating a consumer project.
	create := exec.Command(binary, "new", "missing-runtime")
	create.Dir = outside
	if output, err := create.CombinedOutput(); err == nil || !strings.Contains(string(output), "development CLI requires") {
		t.Fatalf("development runtime selection: %v, %s", err, output)
	}
	consumer := exec.Command("go", "run", "-mod=readonly", ".")
	consumer.Dir = filepath.Join(root, "testdata/configapp")
	output, err := consumer.CombinedOutput()
	if err != nil || string(output) != "localhost 8080 true\n" {
		t.Fatalf("consumer defaults: %v, %s", err, output)
	}
	help := exec.Command(binary, "help")
	help.Dir = outside
	output, err = help.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "Usage:") || strings.Contains(string(output), "koala template") {
		t.Fatalf("relocated help: %v, %s", err, output)
	}
	removed := exec.Command(binary, "template", "project")
	removed.Dir = outside
	output, err = removed.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 2 || !strings.Contains(string(output), "unsupported command") {
		t.Fatalf("removed command: %v, %s", err, output)
	}

	// Consumer Go source is separate from the compiled CLI's dependency graph.
	deps := exec.Command("go", "list", "-f", `{{.ImportPath}} {{join .Deps " "}}`, "./cmd/koala", "./config")
	deps.Dir = root
	graph, err := deps.Output()
	if err != nil {
		t.Fatalf("dependency graphs: %v", err)
	}
	graphs := strings.Split(strings.TrimSpace(string(graph)), "\n")
	if len(graphs) != 2 || !strings.HasPrefix(graphs[0], "github.com/bingyuanng/koala/cmd/koala ") || !strings.HasPrefix(graphs[1], "github.com/bingyuanng/koala/config ") {
		t.Fatalf("unexpected dependency graphs: %s", graph)
	}
	if strings.Contains(graphs[0], "example.com/koala-config-consumer") {
		t.Fatal("application linked into CLI")
	}
	if strings.Contains(graphs[1], "/internal/cli") {
		t.Fatal("runtime depends on CLI")
	}
}
