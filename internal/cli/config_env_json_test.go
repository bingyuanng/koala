package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestEnvironmentJSONConvertsOnlySuppliedVariables(t *testing.T) {
	environment := []string{
		"OTHER_SECRET=not-included", "KOALA_ENV=prod", "KOALA_APP__ENVIRONMENT=prod",
		"KOALA_APP__VERSION=001", "KOALA_SERVER__HTTP__PORT=8080", "KOALA_SERVER__HTTP__ENABLED=false",
		`KOALA_SERVER__HTTP__ALLOWED_ORIGINS=["https://example.test"]`,
		`KOALA_REQUESTS__METHOD_TIMEOUTS={"health.v1.HealthService/Health":"2s"}`,
		"KOALA_TELEMETRY__TRACES__SAMPLE_RATIO=0.25", "KOALA_DATABASE__PASSWORD=001",
		`KOALA_DATABASE__OPTIONS={"identifier":9007199254740993,"enabled":false}`,
		"KOALA_CUSTOM__VALUE=literal=with=equals", "KOALA_CUSTOM__FLAG=false",
	}
	output, err := environmentJSON(environment)
	if err != nil {
		t.Fatal(err)
	}
	expected := `{
 "env":"prod", "app":{"environment":"prod","version":"001"},
 "server":{"http":{"port":8080,"enabled":false,"allowed_origins":["https://example.test"]}},
 "requests":{"method_timeouts":{"health.v1.HealthService/Health":"2s"}},
 "telemetry":{"traces":{"sample_ratio":0.25}},
 "database":{"password":"001","options":{"identifier":9007199254740993,"enabled":false}},
 "custom":{"value":"literal=with=equals","flag":"false"}
 }`
	decode := func(data []byte) any {
		t.Helper()
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if !reflect.DeepEqual(decode(output), decode([]byte(expected))) {
		t.Fatalf("unexpected JSON: %s", output)
	}
	for left, right := 0, len(environment)-1; left < right; left, right = left+1, right-1 {
		environment[left], environment[right] = environment[right], environment[left]
	}
	reversed, err := environmentJSON(environment)
	if err != nil || !bytes.Equal(output, reversed) {
		t.Fatal("conversion depends on environment ordering", err)
	}
}

func TestEnvironmentJSONRejectsAmbiguousPathsAndInvalidTypes(t *testing.T) {
	for _, environment := range [][]string{
		{"KOALA_APP__NAME=SECRET", "KOALA_APP__NAME=SECRET"},
		{"KOALA_app__NAME=SECRET", "KOALA_APP__NAME=SECRET"},
		{`KOALA_SERVER={"http":{"port":8080}}`, "KOALA_SERVER__HTTP__PORT=8080"},
		{"KOALA_SERVER__HTTP__PORT=8080", `KOALA_SERVER={"http":{"port":8080}}`},
		{"KOALA_=SECRET"}, {"KOALA_APP____NAME=SECRET"}, {"KOALA_BAD-NAME=SECRET"},
		{"KOALA_SERVER__HTTP__PORT=SECRET"}, {"KOALA_SERVER__HTTP__ENABLED=SECRET"},
		{`KOALA_REQUESTS__METHOD_TIMEOUTS={"method":99}`},
	} {
		data, err := environmentJSON(environment)
		if err == nil || len(data) != 0 || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("invalid input exposed data or was accepted", err, string(data))
		}
	}
}

func TestEnvironmentJSONDoesNotAddDefaultsOrReadFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "KOALA_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	var out, diagnostics bytes.Buffer
	if err := configCommand([]string{"env", "json"}, &out, &diagnostics); err != nil || out.String() != "{}\n" {
		t.Fatal("empty environment did not produce an empty object", err, out.String())
	}
	t.Setenv("KOALA_APP__ENVIRONMENT", "")
	out.Reset()
	if err := configCommand([]string{"env", "json"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string]string
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal("empty environment value was replaced", err, out.String())
	}
	if value, ok := got["app"]["environment"]; !ok || value != "" {
		t.Fatal("empty environment value was omitted or replaced", out.String())
	}
	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 0 {
		t.Fatal("environment conversion wrote project files", entries, err)
	}
}
