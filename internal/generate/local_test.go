package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bingyuanng/koala/tools"
)

func TestLocalTemplateRejectsUnavailableOrUnpinnedPlugins(t *testing.T) {
	pinned, err := tools.ReadVersions()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, declaration, want string
		invalidBinary           bool
	}{
		{"missing from PATH", "local: protoc-gen-go", "run mise install", false},
		{"invalid binary on PATH", "local: protoc-gen-go", "must be installed from google.golang.org/protobuf@", true},
		{"legacy wrong version", "remote: buf.build/protocolbuffers/go:v0.0.0", "pinned Koala plugin declarations", false},
		{"unknown local plugin", "local: unknown-plugin", "pinned Koala plugin declarations", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "buf.gen.yaml"), []byte("version: v2\nplugins:\n  - "+tc.declaration+"\n    out: gen/go\n"), 0600); err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			t.Setenv("PATH", directory)
			if tc.invalidBinary {
				if err := os.WriteFile(filepath.Join(directory, "protoc-gen-go"), []byte("not a Go generator"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := localTemplate(root, t.TempDir(), pinned); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}
