package scaffold

import (
	"encoding/json"
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
	t.Chdir(t.TempDir())
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

func TestRenderedProjectBuilds(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	for _, kind := range []string{"project", "service"} {
		files, err := Render(kind, Data{
			ModulePath: "example.com/independent", ProjectName: "Display Name",
			ServiceName: "UserService", FrameworkVersion: "v0.1.0",
		})
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
	for _, fixture := range []string{"internal/scaffold/testdata/health/gen/go", "internal/testdata/runtime/gen/go"} {
		if err := os.CopyFS(filepath.Join(project, "gen/go"), os.DirFS(filepath.Join(root, fixture))); err != nil {
			t.Fatal(err)
		}
	}
	// Applications extend their own Config without editing Koala or adding conversions.
	configPath := filepath.Join(project, "internal/config/config.go")
	configSource, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	extended := strings.Replace(string(configSource), "type Config struct {", "type Config struct {\n Database DatabaseConfig `json:\"database\"`", 1)
	extended += "\ntype DatabaseConfig struct { URL string `json:\"url\"`; PoolSize int `json:\"pool_size\"` }\n"
	if err := os.WriteFile(configPath, []byte(extended), 0600); err != nil {
		t.Fatal(err)
	}
	configTest := `package config
import (
 "encoding/json"
 "os"
 "testing"
 koalaconfig "github.com/bingyuanng/koala/config"
)
func TestOwnedConfigDefaults(t *testing.T) {
 expected := koalaconfig.DefaultConfig()
 expected.App.Name = "Display Name"
 expectedJSON, err := json.Marshal(expected)
 if err != nil { t.Fatal(err) }
 ownedJSON, err := json.Marshal(DefaultConfig())
 if err != nil { t.Fatal(err) }
 var owned map[string]json.RawMessage
 if err := json.Unmarshal(ownedJSON, &owned); err != nil { t.Fatal(err) }
 delete(owned, "database")
 var want map[string]json.RawMessage
 if err := json.Unmarshal(expectedJSON, &want); err != nil { t.Fatal(err) }
 for key, value := range want {
  if string(owned[key]) != string(value) { t.Fatalf("defaults differ at %s", key) }
 }
}
func TestOwnedSourcePolicyAndCustomSettings(t *testing.T) {
 t.Chdir(t.TempDir())
 if err := os.WriteFile("config.dev.json", []byte(` + "`" + `{"app":{"name":"from-file"},"database":{"url":"postgres://localhost/app","pool_size":5}}` + "`" + `), 0600); err != nil { t.Fatal(err) }
 for _, environment := range []string{"", "dev", "prod", "staging"} {
  t.Run(environment, func(t *testing.T) {
   t.Setenv("KOALA_ENV", environment)
   t.Setenv("KOALA_LOGGING__LEVEL", "debug")
   t.Setenv("KOALA_DATABASE__POOL_SIZE", "20")
   cfg, err := Load()
   if err != nil { t.Fatal(err) }
   name, url := "Display Name", ""
   if environment == "" || environment == "dev" { name, url = "from-file", "postgres://localhost/app" }
   if cfg.App.Name != name || cfg.Logging.Level != "debug" || cfg.Database.URL != url || cfg.Database.PoolSize != 20 { t.Fatalf("unexpected sources: %#v", cfg) }
  })
 }
 t.Setenv("KOALA_ENV", "prod")
 t.Setenv("KOALA_LOGGING__LEVEL", "invalid")
 if _, err := Load(); err == nil { t.Fatal("invalid logging settings accepted") }
}
`
	if err := os.WriteFile(filepath.Join(project, "internal/config/config_test.go"), []byte(configTest), 0600); err != nil {
		t.Fatal(err)
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
	// Resolve the application graph without compiling it twice. The test below
	// compiles every rendered package and verifies the extended configuration.
	resolve := exec.Command("go", "list", "-mod=mod", "-deps", "./...")
	resolve.Dir = project
	resolve.Env = append(os.Environ(), "GOPROXY=off")
	if output, err := resolve.CombinedOutput(); err != nil {
		t.Fatalf("rendered dependency resolution: %v, %s", err, output)
	}
	verifyConfig := exec.Command("go", "test", "-mod=readonly", "./...")
	verifyConfig.Dir = project
	verifyConfig.Env = []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "KOALA_") {
			verifyConfig.Env = append(verifyConfig.Env, entry)
		}
	}
	if output, err := verifyConfig.CombinedOutput(); err != nil {
		t.Fatalf("rendered project tests: %v, %s", err, output)
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
	for _, name := range []string{"README.md", "cmd/server/main.go", "internal/config/config.go", "internal/deps/deps.go", "internal/bootstrap/doc.go", "internal/service/health/service.go", "proto/health/v1/health.proto", "gen/go/.gitkeep", "gen/openapi/.gitkeep", "buf.yaml", "buf.lock", "buf.gen.yaml", "koala.yaml", "config.dev.json", "mise.toml"} {
		if _, ok := paths[name]; !ok {
			t.Errorf("missing project path %s", name)
		}
	}
	fixture, err := os.ReadFile("testdata/health/proto/health/v1/health.proto")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(fixture), "example.com/sample", data.ModulePath) != paths["proto/health/v1/health.proto"].Content {
		t.Fatal("health transport fixture contract differs from the project template")
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
	if len(service) != 2 || service[0].Path != "internal/service/user/service.go" || service[1].Path != "proto/user/v1/user.proto" {
		t.Fatalf("service layout = %v", service)
	}
}
