package cli

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
	deps := exec.Command("go", "list", "-deps", "./cmd/koala")
	deps.Dir = root
	graph, err := deps.Output()
	if err != nil || strings.Contains(string(graph), "example.com/koala-config-consumer") {
		t.Fatalf("application linked into CLI: %v", err)
	}
	deps = exec.Command("go", "list", "-deps", "./config")
	deps.Dir = root
	graph, err = deps.Output()
	if err != nil || strings.Contains(string(graph), "/internal/cli") {
		t.Fatalf("runtime depends on CLI: %v", err)
	}
}
