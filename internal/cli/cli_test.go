package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
		{[]string{"unknown"}, 2, ""}, {[]string{"help", "extra"}, 2, ""},
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
	for _, tt := range []struct {
		name, version string
		flags         []string
		want          string
		code          int
	}{
		{"missing", "dev", nil, "development CLI requires", 1},
		{"both", "dev", []string{"--framework-version", "v0.2.0", "--framework-path", root}, "choose either", 1},
		{"invalid", "dev", []string{"--framework-path", t.TempDir()}, "must point to", 1},
		{"explicit", "dev", []string{"--framework-version", "v0.2.0"}, "require github.com/bingyuanng/koala v0.2.0", 0},
		{"local", "dev", []string{"--framework-path", root}, "replace github.com/bingyuanng/koala => " + strconv.Quote(root), 0},
		{"release", "v0.3.0", nil, "require github.com/bingyuanng/koala v0.3.0", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			previous := Version
			Version = tt.version
			t.Cleanup(func() { Version = previous })
			var out, stderr bytes.Buffer
			if code := Run(append([]string{"new", "app"}, tt.flags...), &out, &stderr); code != tt.code {
				t.Fatalf("exit %d, want %d; stderr %s", code, tt.code, &stderr)
			}
			if tt.code != 0 {
				if !strings.Contains(stderr.String(), tt.want) {
					t.Fatal(stderr.String())
				}
				if _, err := os.Stat("app"); !os.IsNotExist(err) {
					t.Fatal("invalid runtime selection created a project")
				}
				return
			}
			module, err := os.ReadFile("app/go.mod")
			if err != nil || !strings.Contains(string(module), tt.want) {
				t.Fatalf("module: %s, %v", module, err)
			}
		})
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
	var blockedOut, blockedErr bytes.Buffer
	if code := Run([]string{"new", "blocked", "--framework-path", "."}, &blockedOut, &blockedErr); code != 1 || !strings.Contains(blockedErr.String(), "flag provided but not defined: -framework-path") {
		t.Fatalf("release local override: exit %d, %s", code, blockedErr.String())
	}
	if _, err := os.Stat("blocked"); !os.IsNotExist(err) {
		t.Fatal("release rejection wrote a project")
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

func TestRelocatedCLI(t *testing.T) {
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
	help := exec.Command(binary, "help")
	help.Dir = outside
	output, err := help.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "Usage:") {
		t.Fatalf("relocated help: %v, %s", err, output)
	}
}

func TestConfigDoesNotDependOnCLI(t *testing.T) {
	deps := exec.Command("go", "list", "-f", `{{join .Deps " "}}`, "./config")
	deps.Dir = "../.."
	graph, err := deps.Output()
	if err != nil {
		t.Fatalf("config dependency graph: %v", err)
	}
	if strings.Contains(string(graph), "/internal/cli") {
		t.Fatal("runtime depends on CLI")
	}
}
