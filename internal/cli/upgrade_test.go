package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeExecutable(t *testing.T) {
	for _, tt := range []struct {
		name, script, wantBinary, wantError string
	}{
		{"success", `printf 'new CLI' > "$GOBIN/koala"
chmod 755 "$GOBIN/koala"`, "new CLI", ""},
		{"install failure", `printf 'partial CLI' > "$GOBIN/koala"
echo 'download failed' >&2
exit 7`, "old CLI", "exit status 7"},
		{"missing binary", "exit 0", "old CLI", "replace Koala CLI"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			executable := filepath.Join(dir, "renamed-koala")
			if err := os.WriteFile(executable, []byte("old CLI"), 0755); err != nil {
				t.Fatal(err)
			}
			// The fake Go checks the install target and that GOBIN overrides the
			// user's setting before simulating a download/build.
			script := "#!/bin/sh\nset -eu\n" + `test "$#" = 2
test "$1" = install
test "$2" = github.com/bingyuanng/koala/cmd/koala@latest
test "$GOBIN" != /unused-koala-gobin
` + tt.script + "\n"
			if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+":/bin")
			t.Setenv("GOBIN", "/unused-koala-gobin")
			var out, errout bytes.Buffer
			err := upgradeExecutable(executable, &out, &errout)
			if tt.wantError == "" {
				if err != nil || !strings.Contains(out.String(), "Upgraded Koala CLI at "+executable) {
					t.Fatalf("upgrade: %v; stdout: %s; stderr: %s", err, &out, &errout)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantError) || strings.Contains(out.String(), "Upgraded Koala CLI") {
				t.Fatalf("upgrade: %v; stdout: %s", err, &out)
			}
			contents, err := os.ReadFile(executable)
			if err != nil || string(contents) != tt.wantBinary {
				t.Fatalf("installed binary: %q, %v", contents, err)
			}
			staging, err := filepath.Glob(filepath.Join(dir, ".koala-upgrade-*"))
			if err != nil || len(staging) != 0 {
				t.Fatalf("staging directories remain: %v, %v", staging, err)
			}
		})
	}
}

func TestUpgradeCommands(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, tt := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"upgrade without Go", []string{"upgrade"}, 1, "Go is required"},
		{"update alias without Go", []string{"update"}, 1, "Go is required"},
		{"help without tools", []string{"upgrade", "--help"}, 0, "Usage: koala upgrade"},
		{"unexpected argument", []string{"upgrade", "v1.0.0"}, 1, "usage: koala upgrade"},
		{"unknown flag", []string{"upgrade", "--unknown"}, 1, "flag provided but not defined"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, errout bytes.Buffer
			code := Run(tt.args, &out, &errout)
			if code != tt.code || !strings.Contains(out.String()+errout.String(), tt.want) {
				t.Fatalf("exit %d, stdout: %s, stderr: %s", code, &out, &errout)
			}
		})
	}
}
