package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type appConfig struct {
	Framework Config `json:"framework"`
	Database  struct {
		Password string `json:"password"`
		Count    int    `json:"count"`
		Active   bool   `json:"active"`
	} `json:"database"`
}

func TestExplicitSourcesIgnoreEnvironmentSelector(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(file, []byte(`{"database":{"count":7,"active":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	validated := false
	options := Options[appConfig]{
		Environment: "prod",
		Env:         []string{"KOALA_ENV=prod", "KOALA_DATABASE__COUNT=9"},
		Required:    []string{"database.active"},
		Validate: func(c appConfig) error {
			validated = true
			if c.Database.Count != 9 || !c.Database.Active {
				t.Fatal("file or environment override missing")
			}
			return nil
		},
	}
	if _, err := LoadFile(file, options); err != nil || !validated {
		t.Fatalf("explicit file load: %v, validated %v", err, validated)
	}
	options = Options[appConfig]{
		File: "/must/not/read", Environment: "dev",
		Env: []string{"KOALA_ENV=dev", "KOALA_DATABASE__COUNT=3"},
	}
	got, err := LoadEnv(options)
	if err != nil || got.Database.Count != 3 {
		t.Fatalf("explicit environment load: %#v, %v", got, err)
	}
	if _, err := LoadFile("", options); err == nil {
		t.Fatal("empty file path accepted")
	}
}

func TestDevProdEquivalence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "dev.json")
	if err := os.WriteFile(file, []byte(`{"database":{"password":"001","count":0,"active":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	defaults := func() appConfig {
		c := appConfig{Framework: DefaultConfig()}
		c.Database.Count = 3
		c.Database.Active = true
		return c
	}
	dev, e := LoadWithOptions(Options[appConfig]{Defaults: defaults, File: file, Env: []string{}, Required: []string{"database.count", "database.active"}})
	if e != nil {
		t.Fatal(e)
	}
	prod, e := LoadWithOptions(Options[appConfig]{Defaults: defaults, Environment: "prod", File: "/must/not/read", Env: []string{"KOALA_DATABASE__PASSWORD=001", "KOALA_DATABASE__COUNT=0", "KOALA_DATABASE__ACTIVE=false"}, Required: []string{"database.count", "database.active"}})
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(dev, prod) {
		t.Fatalf("dev/prod differ")
	}
	c, e := LoadWithOptions(Options[appConfig]{Defaults: defaults, File: file, Env: []string{"KOALA_DATABASE__COUNT=5"}})
	if e != nil || c.Database.Count != 5 {
		t.Fatal("override", e)
	}
}
func TestLoaderRejectsInvalidSourcesWithoutSecrets(t *testing.T) {
	tests := []struct {
		name, body, environment string
		env, required           []string
	}{
		{name: "duplicate", body: `{"database":{"password":"SECRET","password":"SECRET"}}`},
		{name: "unknown", body: `{"unknown":"SECRET"}`},
		{name: "type", body: `{"database":{"count":"SECRET"}}`},
		{name: "null", body: `{"database":null}`},
		{name: "required", environment: "prod", required: []string{"database.active"}},
		{name: "env type", environment: "prod", env: []string{"KOALA_DATABASE__COUNT=SECRET"}},
		{name: "env unknown", environment: "prod", env: []string{"KOALA_UNKNOWN=SECRET"}},
		{name: "overlap", environment: "prod", env: []string{`KOALA_DATABASE={"password":"SECRET"}`, "KOALA_DATABASE__PASSWORD=SECRET"}},
		{name: "aggregate unknown", environment: "prod", env: []string{`KOALA_DATABASE={"unknown":"SECRET"}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "dev.json")
			if err := os.WriteFile(file, []byte(tt.body), 0600); err != nil {
				t.Fatal(err)
			}
			_, e := LoadWithOptions(Options[appConfig]{File: file, Environment: tt.environment, Env: tt.env, Required: tt.required})
			if e == nil || strings.Contains(e.Error(), "SECRET") {
				t.Fatalf("expected secret-safe error: %v", e)
			}
		})
	}
}
func TestEnvironmentPathCollisions(t *testing.T) {
	type bad struct {
		One string `json:"name"`
		Two string `json:"NAME"`
	}
	if _, e := LoadWithOptions(Options[bad]{Environment: "prod", Env: []string{}}); e == nil {
		t.Fatal("collision accepted")
	}
}
func TestFrameworkValidation(t *testing.T) {
	for _, tt := range []struct {
		name      string
		change    func(*Config)
		wantError bool
	}{
		{"defaults", func(*Config) {}, false},
		{"negative budget", func(c *Config) { c.Requests.DefaultTimeout = "-1s" }, true},
		{"short write timeout", func(c *Config) { c.Server.HTTP.WriteTimeout = "1s" }, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := DefaultConfig()
			tt.change(&c)
			if err := Validate(c); (err != nil) != tt.wantError {
				t.Fatalf("validation error = %v, want error %v", err, tt.wantError)
			}
		})
	}
}

func TestAggregateOverrideSemantics(t *testing.T) {
	type item struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	type settings struct {
		Object struct {
			Name   string            `json:"name"`
			Values map[string]string `json:"values"`
		} `json:"object"`
		Values map[string]string `json:"values"`
		List   []item            `json:"list"`
		Fixed  [2]item           `json:"fixed"`
	}
	defaults := func() settings {
		c := settings{Values: map[string]string{"old": "value"}, List: []item{{Name: "old", Enabled: true}}}
		c.Fixed = [2]item{{Name: "old", Enabled: true}, {Name: "old", Enabled: true}}
		c.Object.Name = "default"
		c.Object.Values = map[string]string{"old": "value"}
		return c
	}
	for _, tt := range []struct {
		name, file string
		env        []string
		want       map[string]string
	}{
		{"file replacement", `{"object":{"values":{"new":"001"}},"values":{"new":"001"},"list":[{"name":"new"}],"fixed":[{"name":"new"}]}`, nil, map[string]string{"new": "001"}},
		{"file empty maps", `{"object":{"values":{}},"values":{},"list":[{"name":"new"}],"fixed":[{"name":"new"}]}`, nil, map[string]string{}},
		{"environment sections", "", []string{`KOALA_OBJECT={"values":{"new":"001"}}`, `KOALA_VALUES={"new":"001"}`, `KOALA_LIST=[{"name":"new"}]`, `KOALA_FIXED=[{"name":"new"}]`}, map[string]string{"new": "001"}},
		{"environment empty maps", "", []string{`KOALA_OBJECT={"values":{}}`, `KOALA_VALUES={}`, `KOALA_LIST=[{"name":"new"}]`, `KOALA_FIXED=[{"name":"new"}]`}, map[string]string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			options := Options[settings]{Defaults: defaults, Env: tt.env}
			if options.Env == nil {
				options.Env = []string{}
			}
			var got settings
			var err error
			if tt.file != "" {
				file := filepath.Join(t.TempDir(), "config.json")
				if err := os.WriteFile(file, []byte(tt.file), 0600); err != nil {
					t.Fatal(err)
				}
				got, err = LoadFile(file, options)
			} else {
				got, err = LoadEnv(options)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Object.Name != "default" || !reflect.DeepEqual(got.Object.Values, tt.want) || !reflect.DeepEqual(got.Values, tt.want) || !reflect.DeepEqual(got.List, []item{{Name: "new"}}) || got.Fixed != [2]item{{Name: "new"}, {}} {
				t.Fatalf("aggregate replacement retained default entries or lost omitted fields: %#v", got)
			}
		})
	}
}

func TestNamedEmbeddingAndPointerFields(t *testing.T) {
	type app struct {
		Config   `json:"framework"`
		Database *struct {
			Password *string `json:"password"`
		} `json:"database"`
	}
	c, e := LoadWithOptions(Options[app]{Environment: "prod", Env: []string{"KOALA_DATABASE__PASSWORD=001"}})
	if e != nil {
		t.Fatal(e)
	}
	if c.Database == nil || c.Database.Password == nil || *c.Database.Password != "001" {
		t.Fatal("pointer field not decoded")
	}
}

func TestDefaultLoaderSourcesAndValidation(t *testing.T) {
	t.Chdir(t.TempDir())
	// Give the default loader a controlled process environment.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "KOALA_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile("config.dev.json", []byte(`{"app":{"name":"from-file"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	defaults := func() Config { cfg := DefaultConfig(); cfg.App.Name = "from-defaults"; return cfg }
	cfg, err := Load(defaults, Validate)
	if err != nil || cfg.App.Name != "from-file" {
		t.Fatal(cfg.App.Name, err)
	}
	t.Setenv("KOALA_APP__NAME", "from-env")
	cfg, err = Load(defaults, Validate)
	if err != nil || cfg.App.Name != "from-env" {
		t.Fatal(cfg.App.Name, err)
	}
	if err := os.Remove("config.dev.json"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KOALA_ENV", "prod")
	cfg, err = Load(defaults, Validate)
	if err != nil || cfg.App.Name != "from-env" {
		t.Fatal(cfg.App.Name, err)
	}
	rejected := errors.New("application validation failed")
	calls := 0
	_, err = Load(defaults, func(cfg Config) error {
		calls++
		if cfg.App.Name != "from-env" {
			t.Fatal("validator ran before source merging")
		}
		return rejected
	})
	if !errors.Is(err, rejected) || calls != 1 {
		t.Fatal(err, calls)
	}
	t.Setenv("KOALA_ENV", "dev")
	if _, err := Load(defaults, Validate); err == nil {
		t.Fatal("missing development file accepted")
	}
}

func TestOnlyDevReadsConfigurationFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.dev.json")
	if err := os.WriteFile(file, []byte(`{"app":{"name":"from-file"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	defaults := func() Config {
		cfg := DefaultConfig()
		cfg.App.Name = "from-defaults"
		return cfg
	}
	for _, environment := range []string{"", "dev", "prod", "staging", "test", "development"} {
		cfg, err := LoadWithOptions(Options[Config]{Defaults: defaults, File: file, Env: []string{"KOALA_ENV=" + environment}})
		if err != nil {
			t.Fatal(environment, err)
		}
		want := "from-defaults"
		if environment == "" || environment == "dev" {
			want = "from-file"
		}
		if cfg.App.Name != want {
			t.Errorf("environment %q: got %q, want %q", environment, cfg.App.Name, want)
		}
	}
	cfg, err := LoadWithOptions(Options[Config]{Defaults: defaults, Environment: "dev", File: file, Env: []string{"KOALA_ENV=prod"}})
	if err != nil || cfg.App.Name != "from-file" {
		t.Fatal("explicit environment did not take precedence", cfg.App.Name, err)
	}
}
