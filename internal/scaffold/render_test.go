package scaffold

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/bingyuanng/koala/config"
)

func TestEmbeddedTemplates(t *testing.T) {
	for _, kind := range []string{"project", "service"} {
		data := Data{ModulePath: "example.com/import-path", ProjectName: "Display Name", ServiceName: "Accounts", FrameworkVersion: "v0.1.2"}
		files, err := Render(kind, data)
		if err != nil {
			t.Fatal(err)
		}
		again, err := Render(kind, data)
		if err != nil || !reflect.DeepEqual(files, again) {
			t.Fatal("rendering is not deterministic")
		}
		joined := ""
		foundDotfile := false
		for _, f := range files {
			if f.Ownership != "application" && !(strings.HasPrefix(f.Path, "gen/") && f.Ownership == "generated") {
				t.Fatalf("incorrect ownership for %s: %s", f.Path, f.Ownership)
			}
			joined += f.Content
			if f.Path == ".gitignore" {
				foundDotfile = true
			}
			if strings.HasSuffix(f.Path, ".go") {
				if _, err := parser.ParseFile(token.NewFileSet(), f.Path, f.Content, parser.AllErrors); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasSuffix(f.Path, ".tmpl") {
				t.Fatal("template suffix leaked")
			}
		}
		if !strings.Contains(joined, data.ModulePath) {
			t.Fatal("module path lost")
		}
		if kind == "project" && (!foundDotfile || !strings.Contains(joined, data.ProjectName) || !strings.Contains(joined, data.FrameworkVersion)) {
			t.Fatal("project data or dotfile lost")
		}
		if kind == "service" && (!strings.Contains(joined, "UnimplementedAccountsServer") || !strings.Contains(joined, "//koala:service accounts.v1.Accounts") || !strings.Contains(joined, data.ModulePath+"/gen/go/accounts/v1")) {
			t.Fatal("service mapping lost")
		}
	}
}

func TestRenderErrorsAreAtomic(t *testing.T) {
	tests := []struct {
		name     string
		root     fstest.MapFS
		data     any
		fragment string
	}{
		{"missing value", fstest.MapFS{"file.tmpl": {Data: []byte(`{{.Missing}}`)}}, map[string]string{}, "Missing"},
		{"invalid syntax", fstest.MapFS{"file.tmpl": {Data: []byte(`{{`)}}, Data{}, "parse template"},
		{"traversal", fstest.MapFS{"{{.Path}}.tmpl": {Data: []byte(`ok`)}}, map[string]string{"Path": "../escape"}, "unsafe output path"},
		{"absolute", fstest.MapFS{"{{.Path}}.tmpl": {Data: []byte(`ok`)}}, map[string]string{"Path": "/escape"}, "unsafe output path"},
		{"windows", fstest.MapFS{"{{.Path}}.tmpl": {Data: []byte(`ok`)}}, map[string]string{"Path": `C:\escape`}, "unsafe output path"},
		{"collision", fstest.MapFS{"a": {Data: []byte(`one`)}, "a.tmpl": {Data: []byte(`two`)}}, Data{}, "duplicate output path"},
		{"invalid Go", fstest.MapFS{"file.go.tmpl": {Data: []byte(`package`)}}, Data{}, "format Go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, err := renderFS(tt.root, tt.data)
			if err == nil || !strings.Contains(err.Error(), tt.fragment) || files != nil {
				t.Fatalf("files=%v err=%v", files, err)
			}
		})
	}
	if _, err := Render("project", Data{}); err == nil {
		t.Fatal("empty inputs accepted")
	}
}

func TestEmbeddedAssetsOutsideRepository(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	binary := filepath.Join(outside, "scaffold.test")
	build := exec.Command("go", "test", "-c", "-o", binary, "./internal/scaffold")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build asset test: %v, %s", err, output)
	}
	check := exec.Command(binary, "-test.run=^TestEmbeddedTemplates$")
	check.Dir = outside
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("relocated assets: %v, %s", err, output)
	}
}

func TestRenderedProjectBuilds(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	files, err := Render("project", Data{
		ModulePath: "example.com/independent", ProjectName: "Display Name",
		ServiceName: "Accounts", FrameworkVersion: "v0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	for _, file := range files {
		target := filepath.Join(project, file.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(file.Content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	edit := exec.Command("go", "mod", "edit", "-replace=github.com/bingyuanng/koala="+root)
	edit.Dir = project
	if output, err := edit.CombinedOutput(); err != nil {
		t.Fatalf("project module: %v, %s", err, output)
	}
	checksums, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(project, "go.sum"), checksums, 0600); err != nil {
		t.Fatal(err)
	}
	resolve := exec.Command("go", "build", "-mod=mod", "./...")
	resolve.Dir = project
	resolve.Env = append(os.Environ(), "GOPROXY=off")
	if output, err := resolve.CombinedOutput(); err != nil {
		t.Fatalf("rendered dependency resolution: %v, %s", err, output)
	}
	build := exec.Command("go", "build", "-mod=readonly", "./...")
	build.Dir = project
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("rendered project build: %v, %s", err, output)
	}
}

func TestProjectLayoutAndDefaults(t *testing.T) {
	data := Data{ModulePath: "example.com/app", ProjectName: `quoted "name"`, ServiceName: "UserService", FrameworkVersion: "v0.1.0"}
	files, err := Render("project", data)
	if err != nil {
		t.Fatal(err)
	}
	paths := make(map[string]File)
	for _, file := range files {
		paths[file.Path] = file
	}
	for _, name := range []string{"cmd/server/main.go", "internal/deps/deps.go", "internal/bootstrap/doc.go", "gen/go/.gitkeep", "gen/openapi/.gitkeep", "buf.yaml", "buf.lock", "buf.gen.yaml", "koala.yaml", "config.dev.json"} {
		if _, ok := paths[name]; !ok {
			t.Errorf("missing project path %s", name)
		}
	}
	var cfg config.Config
	if err := json.Unmarshal([]byte(paths["config.dev.json"].Content), &cfg); err != nil {
		t.Fatal(err)
	}
	expected := config.DefaultConfig()
	expected.App.Name = data.ProjectName
	if !reflect.DeepEqual(cfg, expected) {
		t.Fatalf("template defaults = %#v, want %#v", cfg, expected)
	}
	service, err := Render("service", data)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range service {
		if file.Ownership != "application" {
			t.Fatalf("service source is not application-owned: %s", file.Path)
		}
	}
	if len(service) != 2 || service[0].Path != "internal/service/user/service.go" || service[1].Path != "proto/user/v1/user.proto" {
		t.Fatalf("service layout = %v", service)
	}
}
