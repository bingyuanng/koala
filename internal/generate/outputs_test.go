package generate

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFailedGenerationRestoresOutputsAndCanRetry(t *testing.T) {
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOWORK", "off")
	root := t.TempDir()
	files := map[string][]byte{
		"go.mod":     []byte("module example.com/app\n\ngo 1.26.9\n"),
		"koala.yaml": []byte("version: v1\nproto_root: proto\nservice_packages: ./internal/service/...\ndependency_type: example.com/app/internal/missing.Dependencies\nregistry:\n  package: bootstrap\n  output: internal/bootstrap/services_gen.go\n"),
		"buf.yaml":   nil, "buf.lock": nil, "buf.gen.yaml": nil,
		"gen/go/old.pb.go":                   []byte(header + "package old\n"),
		"internal/bootstrap/services_gen.go": []byte(header + "package bootstrap\n"),
		".koala/descriptors.bin":             nil,
	}
	previous := manifest{Version: 1, Outputs: map[string]string{}}
	for _, path := range []string{"gen/go/old.pb.go", "internal/bootstrap/services_gen.go", ".koala/descriptors.bin"} {
		previous.Outputs[path] = hash(files[path])
	}
	var err error
	files[manifestPath], err = json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	for path, data := range files {
		dest := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// An empty contract removes old transports, then fails the registry build
	// because the application dependency package is missing. Each retry must get
	// as far as that build instead of rejecting our own partially updated files.
	for attempt := 0; attempt < 2; attempt++ {
		err := Run(context.Background(), root, Options{})
		if err == nil || !strings.Contains(err.Error(), "failed its consumer build") {
			t.Fatalf("attempt %d did not reach registry validation: %v", attempt, err)
		}
		for path, want := range files {
			got, err := readOutput(filepath.Join(root, path))
			if err != nil || !got.exists || got.mode != 0600 || !bytes.Equal(got.data, want) {
				t.Fatalf("attempt %d changed %s after failure: %v", attempt, path, err)
			}
		}
	}
}

func TestOutputRollbackPreservesConcurrentEdits(t *testing.T) {
	root := t.TempDir()
	txn := newOutputTransaction(root)
	path := "gen/go/new.pb.go"
	if err := txn.write(path, []byte("generated")); err != nil {
		t.Fatal(err)
	}
	edit := []byte("application edit")
	if err := os.WriteFile(filepath.Join(root, path), edit, 0644); err != nil {
		t.Fatal(err)
	}
	if err := txn.rollback(); err == nil {
		t.Fatal("rollback overwrote a concurrent edit")
	}
	got, err := os.ReadFile(filepath.Join(root, path))
	if err != nil || !bytes.Equal(got, edit) {
		t.Fatal("concurrent edit lost", err)
	}
}
