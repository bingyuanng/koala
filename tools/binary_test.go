package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveBinary(t *testing.T) {
	for _, tc := range []struct {
		name string
		shim bool
	}{
		{name: "direct executable"},
		{name: "mise shim", shim: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bin := t.TempDir(), t.TempDir()
			mise := filepath.Join(bin, "mise")
			// Check the command and working directory as well as the returned path.
			script := "#!/bin/sh\n[ \"$1\" = which ] && [ \"$2\" = protoc-gen-go ] || exit 2\nprintf '%s\\n' \"$PWD/installed-generator\"\n"
			if err := os.WriteFile(mise, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(bin, "protoc-gen-go")
			want := binary
			if tc.shim {
				if err := os.Symlink(mise, binary); err != nil {
					t.Fatal(err)
				}
				want = filepath.Join(root, "installed-generator")
			} else if err := os.WriteFile(binary, []byte("direct generator"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			got, err := ResolveBinary(root, "protoc-gen-go")
			if err != nil || got != want {
				t.Fatalf("ResolveBinary = %q, %v; want %q", got, err, want)
			}
		})
	}
}
