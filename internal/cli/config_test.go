package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bingyuanng/koala/config"
)

func configEnvProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "koala.yaml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "cmd/server")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	return root
}

func TestConfigEnvConvertsSelectedFileAndRoundTrips(t *testing.T) {
	root := configEnvProject(t)
	source := `{"app":{"name":"production","environment":"production"},"server":{"http":{"port":8088,"allowed_origins":["https://example.test"]}},"requests":{"method_timeouts":{"health.v1.HealthService/Health":"2s"}},"database":{"password":"a'b $(echo literal)\nnext","options":{"pool":"large"}},"big":9007199254740993,"zero":0,"flag":false,"names":[],"empty":{}}`
	for name, body := range map[string]string{"config.prd.json": source, "config.dev.json": `{"app":{"name":"development"}}`} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("KOALA_APP__NAME", "ambient-value-must-not-appear")
	var out bytes.Buffer
	if err := configCommand([]string{"prd", "env"}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Contains(text, "ambient-value") || strings.Contains(text, "development") || !strings.Contains(text, "KOALA_ENV=prd") {
		t.Fatal("wrong configuration sources", text)
	}
	if !strings.Contains(text, "KOALA_SERVER__HTTP__PORT='8088'") || strings.Contains(text, "KOALA_REQUESTS__METHOD_TIMEOUTS__") || strings.Contains(text, "KOALA_LOGGING") {
		t.Fatal("incorrect flattening or added defaults", text)
	}
	var again bytes.Buffer
	if err := configCommand([]string{"prd", "env"}, &again); err != nil || out.String() != again.String() {
		t.Fatal("nondeterministic conversion", err)
	}
	dotenv := filepath.Join(root, "result.env")
	if err := os.WriteFile(dotenv, out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	shell := exec.Command("sh", "-c", `set -a; . "$1"; env -0`, "sh", dotenv)
	shell.Env = []string{"PATH=" + os.Getenv("PATH")}
	exported, err := shell.Output()
	if err != nil {
		t.Fatal(err)
	}
	var environment []string
	for _, entry := range bytes.Split(exported, []byte{0}) {
		if len(entry) > 0 {
			environment = append(environment, string(entry))
		}
	}
	type application struct {
		App      config.AppConfig     `json:"app"`
		Server   config.ServerConfig  `json:"server"`
		Requests config.RequestConfig `json:"requests"`
		Database struct {
			Password string            `json:"password"`
			Options  map[string]string `json:"options"`
		} `json:"database"`
		Big   int64             `json:"big"`
		Zero  int               `json:"zero"`
		Flag  bool              `json:"flag"`
		Names []string          `json:"names"`
		Empty map[string]string `json:"empty"`
	}
	var want application
	if err := json.Unmarshal([]byte(source), &want); err != nil {
		t.Fatal(err)
	}
	got, err := config.LoadWithOptions(config.Options[application]{Env: environment})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("conversion did not preserve configuration values: got %#v, want %#v", got, want)
	}
}

func TestConfigEnvRejectsInvalidInputsWithoutOutput(t *testing.T) {
	root := configEnvProject(t)
	for _, args := range [][]string{nil, {"../prod"}, {"prod/other"}, {"prod.json"}, {"--output"}, {"prod", "--output", "unexpected.env"}, {"prd", "--force"}, {"json"}, {"env", "json"}, {"env", "prd"}, {"prd", "json"}, {"prd", "env", "extra"}, {"stg"}} {
		var out bytes.Buffer
		if err := configCommand(args, &out); err == nil || out.Len() != 0 {
			t.Fatalf("%v: %v, output %s", args, err, out.String())
		}
	}
	if _, err := os.Stat("unexpected.env"); !os.IsNotExist(err) {
		t.Fatal("rejected output flag wrote a file")
	}
	for _, body := range []string{
		`{"password":"SECRET","password":"SECRET"}`, `{"database":null}`, `[]`, `{} {}`,
		`{"app":{"name":"SECRET"}`, `{"app":{"name":"SECRET"},"APP":{"name":"SECRET"}}`,
		`{"name":"\u0000SECRET"}`, `{"bad-name":"SECRET"}`, `{"bad__name":"SECRET"}`, `{"env":"SECRET"}`,
	} {
		if err := os.WriteFile(filepath.Join(root, "config.prd.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err := configCommand([]string{"prd", "env"}, &out)
		if err == nil || out.Len() != 0 || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("invalid input produced output or exposed a value: %v, %s", err, out.String())
		}
	}
}

func TestConfigJSONSelectsFileWithoutMerging(t *testing.T) {
	root := configEnvProject(t)
	t.Setenv("KOALA_ENV", "dev")
	t.Setenv("KOALA_APP__NAME", "ambient")
	for _, environment := range []string{"dev", "stg", "prd"} {
		source := `{"app":{"name":"` + environment + `"},"large":9007199254740993,"enabled":false}`
		if err := os.WriteFile(filepath.Join(root, "config."+environment+".json"), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, environment := range []string{"dev", "stg", "prd"} {
		t.Run(environment, func(t *testing.T) {
			var out bytes.Buffer
			if err := configCommand([]string{environment}, &out); err != nil {
				t.Fatal(err)
			}
			want := "{\n  \"app\": {\n    \"name\": \"" + environment + "\"\n  },\n  \"enabled\": false,\n  \"large\": 9007199254740993\n}\n"
			if out.String() != want {
				t.Fatalf("got %s, want %s", out.String(), want)
			}
		})
	}
	for _, source := range []string{`[]`, `{"x":null}`, `{"x":1,"x":2}`, `{} {}`} {
		if err := os.WriteFile(filepath.Join(root, "config.prd.json"), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := configCommand([]string{"prd"}, &out); err == nil || out.Len() != 0 {
			t.Fatal("invalid JSON accepted or partially printed", err)
		}
	}
}
