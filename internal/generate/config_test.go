package generate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadConfig(t *testing.T) {
	const minimal = "version: v1\ndependency_type: example.com/app/internal/resources.Resources\n"
	for _, tc := range []struct {
		name, source, wantError string
		exclude                 []string
	}{
		{name: "minimal", source: minimal},
		{name: "exclude services", source: minimal + "exclude: [user.v1.UserService]\n", exclude: []string{"user.v1.UserService"}},
		{name: "removed proto root", source: minimal + "proto_root: proto\n", wantError: "proto_root"},
		{name: "removed service packages", source: minimal + "service_packages: ./internal/service/...\n", wantError: "service_packages"},
		{name: "removed registry package", source: minimal + "registry:\n  package: bootstrap\n", wantError: "registry"},
		{name: "removed registry output", source: minimal + "registry:\n  output: internal/bootstrap/services_gen.go\n", wantError: "registry"},
		{name: "unknown field", source: minimal + "typo: true\n", wantError: "typo"},
		{name: "unsupported version", source: strings.Replace(minimal, "v1", "v2", 1), wantError: "version must be v1"},
		{name: "missing dependency", source: "version: v1\n", wantError: "dependency_type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "koala.yaml"), []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := readConfig(root)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected %q, got %v", tc.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Version != "v1" || cfg.DependencyType != "example.com/app/internal/resources.Resources" || !reflect.DeepEqual(cfg.Exclude, tc.exclude) {
				t.Fatalf("unexpected configuration: %+v", cfg)
			}
		})
	}
}
