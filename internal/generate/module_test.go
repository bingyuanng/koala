package generate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplicationImportPath(t *testing.T) {
	for _, tt := range []struct {
		name, module, subdir, want string
	}{
		{"module", "module example.com/app\n", "", "example.com/app"},
		{"quoted module in monorepo", "// Application module\nmodule \"example.com/app\" // comment\n", "services/users", "example.com/app/services/users"},
		{"invalid syntax", "module \"unterminated\n", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(tt.module), 0600); err != nil {
				t.Fatal(err)
			}
			project := filepath.Join(root, tt.subdir)
			if err := os.MkdirAll(project, 0700); err != nil {
				t.Fatal(err)
			}
			got, err := ApplicationImportPath(project)
			if (err != nil) != (tt.want == "") || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
