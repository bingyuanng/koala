package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The same arbitrary section must have identical acceptance and merge semantics
// in files and environment overrides, including pointers, maps and slice values.
func FuzzConfigurationSources(f *testing.F) {
	for _, seed := range []string{
		`{}`, `{"name":"001","count":0,"enabled":false}`,
		`{"values":{"A":9007199254740993,"a":-1},"rows":[{"name":"row"}]}`,
		`{"NAME":"loaded","values":{},"rows":[]}`, `{"count":9223372036854775808}`,
		`{"name":"one","NAME":"two"}`, `{"values":{"a":1,"a":2}}`, `{"rows":[null]}`,
		`null`, `[]`, `{"unknown":"value"}`, `{} {}`, `{"name":`,
	} {
		f.Add(seed)
	}
	type row struct {
		Name string `json:"name"`
	}
	type section struct {
		Name    string           `json:"name"`
		Count   int64            `json:"count"`
		Enabled bool             `json:"enabled"`
		Values  map[string]int64 `json:"values"`
		Rows    []row            `json:"rows"`
		Ignored string           `json:"-"`
		private string
	}
	type configuration struct {
		Database *section `json:"database"`
	}
	defaults := func() configuration {
		return configuration{Database: &section{
			Name: "default", Count: 7, Enabled: true, Values: map[string]int64{"old": 1},
			Rows: []row{{Name: "default"}}, Ignored: "ignored", private: "private",
		}}
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 64<<10 {
			t.Skip()
		}
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{"database":`+source+`}`), 0600); err != nil {
			t.Fatal(err)
		}
		file, fileErr := LoadFile(path, Options[configuration]{Defaults: defaults, Env: []string{}})
		env, envErr := LoadEnv(Options[configuration]{Defaults: defaults, Env: []string{"KOALA_DATABASE=" + source}})
		if (fileErr == nil) != (envErr == nil) || !reflect.DeepEqual(file, env) {
			t.Fatalf("source semantics differ: file=%#v (%v), environment=%#v (%v)", file, fileErr, env, envErr)
		}
		if fileErr != nil {
			if file.Database != nil {
				t.Fatal("failed load returned partial configuration")
			}
			return
		}
		if file.Database == nil || file.Database.Ignored != "ignored" || file.Database.private != "private" {
			t.Fatal("source replaced constructor-only defaults")
		}
	})
}
