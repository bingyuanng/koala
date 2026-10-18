package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveIsReproducibleAndExecutable(t *testing.T) {
	dir := t.TempDir()
	binary := []byte("CLI binary payload\x00\xff")
	var previous []byte
	for _, name := range []string{"first.tar.gz", "second.tar.gz"} {
		path := filepath.Join(dir, name)
		writeArchive(path, binary)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if previous != nil && !bytes.Equal(previous, data) {
			t.Fatal("identical binaries produced different archives")
		}
		previous = data
		compressed, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if !compressed.ModTime.IsZero() || compressed.Name != "" {
			t.Fatalf("gzip header contains variable metadata: %+v", compressed.Header)
		}
		archive := tar.NewReader(compressed)
		header, err := archive.Next()
		if err != nil {
			t.Fatal(err)
		}
		if header.Name != "koala" || header.Mode != 0755 || header.Size != int64(len(binary)) || header.ModTime.Unix() != 0 {
			t.Fatalf("unexpected archive header: %+v", header)
		}
		content, err := io.ReadAll(archive)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, binary) {
			t.Fatal("archive changed binary contents")
		}
		if _, err := archive.Next(); err != io.EOF {
			t.Fatalf("unexpected extra archive entry: %v", err)
		}
		if err := compressed.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
