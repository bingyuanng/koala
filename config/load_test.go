package config

import (
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

func TestLocalCloudEquivalence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "dev.json")
	os.WriteFile(file, []byte(`{"database":{"password":"001","count":0,"active":false}}`), 0600)
	defaults := func() appConfig {
		c := appConfig{Framework: DefaultConfig()}
		c.Database.Count = 3
		c.Database.Active = true
		return c
	}
	local, e := Load(Options[appConfig]{Defaults: defaults, File: file, Env: []string{}, Required: []string{"database.count", "database.active"}})
	if e != nil {
		t.Fatal(e)
	}
	cloud, e := Load(Options[appConfig]{Defaults: defaults, Mode: "cloud", File: "/must/not/read", Env: []string{"KOALA_DATABASE__PASSWORD=001", "KOALA_DATABASE__COUNT=0", "KOALA_DATABASE__ACTIVE=false"}, Required: []string{"database.count", "database.active"}})
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(local, cloud) {
		t.Fatalf("local/cloud differ")
	}
	c, e := Load(Options[appConfig]{Defaults: defaults, File: file, Env: []string{"KOALA_DATABASE__COUNT=5"}})
	if e != nil || c.Database.Count != 5 {
		t.Fatal("override", e)
	}
}
func TestLoaderRejectsInvalidSourcesWithoutSecrets(t *testing.T) {
	tests := []struct {
		name, body, mode string
		env, required    []string
	}{
		{name: "duplicate", body: `{"database":{"password":"SECRET","password":"SECRET"}}`},
		{name: "unknown", body: `{"unknown":"SECRET"}`},
		{name: "type", body: `{"database":{"count":"SECRET"}}`},
		{name: "null", body: `{"database":null}`},
		{name: "mode", mode: "SECRET"},
		{name: "required", mode: "cloud", required: []string{"database.active"}},
		{name: "env type", mode: "cloud", env: []string{"KOALA_DATABASE__COUNT=SECRET"}},
		{name: "env unknown", mode: "cloud", env: []string{"KOALA_UNKNOWN=SECRET"}},
		{name: "overlap", mode: "cloud", env: []string{`KOALA_DATABASE={"password":"SECRET"}`, "KOALA_DATABASE__PASSWORD=SECRET"}},
		{name: "aggregate unknown", mode: "cloud", env: []string{`KOALA_DATABASE={"unknown":"SECRET"}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "dev.json")
			os.WriteFile(file, []byte(tt.body), 0600)
			_, e := Load(Options[appConfig]{File: file, Mode: tt.mode, Env: tt.env, Required: tt.required})
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
	if _, e := Load(Options[bad]{Mode: "cloud", Env: []string{}}); e == nil {
		t.Fatal("collision accepted")
	}
}
func TestFrameworkValidation(t *testing.T) {
	c := DefaultConfig()
	if e := Validate(c); e != nil {
		t.Fatal(e)
	}
	c.Requests.DefaultTimeout = "-1s"
	if Validate(c) == nil {
		t.Fatal("negative budget accepted")
	}
	c = DefaultConfig()
	c.Server.HTTP.WriteTimeout = "1s"
	if Validate(c) == nil {
		t.Fatal("short write timeout accepted")
	}
}

func TestAggregateOverrideSemantics(t *testing.T) {
	type settings struct {
		Object struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"object"`
		Values map[string]string `json:"values"`
		List   []string          `json:"list"`
	}
	defaults := func() settings {
		c := settings{Values: map[string]string{"old": "value"}, List: []string{"old"}}
		c.Object.Name = "default"
		c.Object.Enabled = true
		return c
	}
	c, e := Load(Options[settings]{Defaults: defaults, Mode: "cloud", Env: []string{`KOALA_OBJECT={"enabled":false}`, `KOALA_VALUES={"new":"001"}`, `KOALA_LIST=["first","second"]`}, Required: []string{"object.enabled"}})
	if e != nil {
		t.Fatal(e)
	}
	if c.Object.Name != "default" || c.Object.Enabled || len(c.Values) != 1 || c.Values["new"] != "001" || len(c.List) != 2 {
		t.Fatalf("aggregate semantics %#v", c)
	}
}

func TestNamedEmbeddingAndPointerFields(t *testing.T) {
	type app struct {
		Config   `json:"framework"`
		Database *struct {
			Password *string `json:"password"`
		} `json:"database"`
	}
	c, e := Load(Options[app]{Mode: "cloud", Env: []string{"KOALA_DATABASE__PASSWORD=001"}})
	if e != nil {
		t.Fatal(e)
	}
	if c.Database == nil || c.Database.Password == nil || *c.Database.Password != "001" {
		t.Fatal("pointer field not decoded")
	}
}
