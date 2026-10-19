package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMergeUsesCanonicalFieldsAndPreservesConstructorDefaults(t *testing.T) {
	type database struct {
		Name    string           `json:"name"`
		Values  map[string]int64 `json:"values"`
		Ignored string           `json:"-"`
		private string
	}
	type settings struct {
		Database *database  `json:"database"`
		Unused   []database `json:"unused"`
		Ignored  string     `json:"-"`
		private  string
	}
	defaults := func() settings {
		return settings{
			Database: &database{Name: "default", Values: map[string]int64{"old": 1}, Ignored: "nested ignored", private: "nested private"},
			Unused:   []database{{private: "omitted aggregate"}},
			Ignored:  "ignored", private: "private",
		}
	}
	values := map[string]int64{"A": 9007199254740993, "a": 2}
	for _, source := range []string{"file", "environment section", "environment fields"} {
		t.Run(source, func(t *testing.T) {
			options := Options[settings]{Defaults: defaults, Env: []string{}, Required: []string{"database.name"}}
			var got settings
			var err error
			switch source {
			case "file":
				file := filepath.Join(t.TempDir(), "config.json")
				if err := os.WriteFile(file, []byte(`{"DATABASE":{"NAME":"loaded","VALUES":{"A":9007199254740993,"a":2}}}`), 0600); err != nil {
					t.Fatal(err)
				}
				got, err = LoadFile(file, options)
			case "environment section":
				options.Env = []string{`KOALA_DATABASE={"NAME":"loaded","VALUES":{"A":9007199254740993,"a":2}}`}
				got, err = LoadEnv(options)
			case "environment fields":
				options.Env = []string{"KOALA_DATABASE__NAME=loaded", `KOALA_DATABASE__VALUES={"A":9007199254740993,"a":2}`}
				got, err = LoadEnv(options)
			}
			if err != nil {
				t.Fatal(err)
			}
			want := defaults()
			want.Database.Name, want.Database.Values = "loaded", values
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("merged config = %#v; database = %#v", got, got.Database)
			}
		})
	}
}

func TestEnvironmentMergeSupportsJSONDecodersAndQuotedFields(t *testing.T) {
	type settings struct {
		Timestamp time.Time `json:"timestamp"`
		Count     int64     `json:"count,string"`
		Label     string    `json:"label,string"`
	}
	got, err := LoadEnv(Options[settings]{Env: []string{
		`KOALA_TIMESTAMP="2026-10-19T00:00:00Z"`,
		"KOALA_COUNT=9007199254740993", "KOALA_LABEL=loaded",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Timestamp != time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC) || got.Count != 9007199254740993 || got.Label != "loaded" {
		t.Fatalf("unexpected decoded configuration: %#v", got)
	}
}

func TestMergeRejectsAmbiguousFieldsInsideContainers(t *testing.T) {
	type item struct {
		Name string `json:"name"`
	}
	type settings struct {
		Rows  []item          `json:"rows"`
		ByKey map[string]item `json:"by_key"`
	}
	for _, body := range []string{
		`{"rows":[{"name":"SECRET","NAME":"SECRET"}]}`,
		`{"by_key":{"key":{"name":"SECRET","NAME":"SECRET"}}}`,
	} {
		file := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadFile(file, Options[settings]{Env: []string{}})
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("ambiguous field was accepted or exposed a value: %v", err)
		}
	}
}
