package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The shell is compiled against the checked-in UserService transport fixture.
// No generation tool or remote service is required by ordinary tests.
func TestRenderedServiceBuilds(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	data := Data{ModulePath: "example.com/shell", ProjectName: "shell", ServiceName: "UserService", FrameworkVersion: "v0.1.0"}
	for _, kind := range []string{"project", "service"} {
		files, err := Render(kind, data)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			target := filepath.Join(project, file.Path)
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte(file.Content), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	copyHealthTransportFixture(t, root, project)
	source := filepath.Join(root, "internal/testdata/runtime/gen/go/user/v1")
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(project, "gen/go/user/v1")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, entry.Name()), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	checksums, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "go.sum"), checksums, 0600); err != nil {
		t.Fatal(err)
	}
	// Use this checkout and its cached dependencies rather than a published release.
	// A build resolves only application dependencies. Tidy also loads dependency
	// tests, whose modules need not be cached by an ordinary repository build.
	for _, args := range [][]string{
		{"mod", "edit", "-replace=github.com/bingyuanng/koala=" + root},
		{"build", "-mod=mod", "./..."},
		{"build", "-mod=readonly", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = project
		cmd.Env = append(os.Environ(), "GOPROXY=off")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v, %s", args, err, output)
		}
	}
}
