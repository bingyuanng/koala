// Build versioned CLI archives and checksums without publishing a release.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	s "github.com/bingyuanng/koala/scripts/internal/scriptutil"
)

func main() { s.Main(build) }
func build() {
	root := s.Root()
	version := flag.String("version", "", "Explicit semantic release version")
	output := flag.String("output", filepath.Join(root, "dist"), "Archive output directory")
	selectedTarget := flag.String("target", "", "Build only linux/amd64, linux/arm64, darwin/amd64, or darwin/arm64; default builds all targets")
	flag.Parse()
	s.Check(regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-[a-zA-Z0-9.-]+)?$`).MatchString(*version), "version must be an explicit semantic release version")
	s.Check(flag.NArg() == 0, "unexpected arguments: %v", flag.Args())
	targets := [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}}
	if *selectedTarget != "" {
		var selected [][2]string
		for _, target := range targets {
			if target[0]+"/"+target[1] == *selectedTarget {
				selected = append(selected, target)
			}
		}
		s.Check(len(selected) == 1, "unsupported target %q; use linux/amd64, linux/arm64, darwin/amd64, or darwin/arm64", *selectedTarget)
		targets = selected
	}
	s.Must(os.MkdirAll(*output, 0755))
	checksums := []string{}
	for _, target := range targets {
		func() {
			dir := s.Temp("koala-release-")
			defer os.RemoveAll(dir)
			binary := filepath.Join(dir, "koala")
			env := s.Env(map[string]string{"GOOS": target[0], "GOARCH": target[1], "CGO_ENABLED": "0"}, "")
			s.Run(root, env, false, "go", "build", "-mod=readonly", "-trimpath", "-ldflags", "-s -w -X github.com/bingyuanng/koala/internal/cli.Version="+*version, "-o", binary, "./cmd/koala")
			name := fmt.Sprintf("koala_%s_%s_%s.tar.gz", *version, target[0], target[1])
			path := filepath.Join(*output, name)
			writeArchive(path, []byte(s.Read(binary)))
			checksums = append(checksums, fmt.Sprintf("%x  %s", sha256.Sum256([]byte(s.Read(path))), name))
			fmt.Println(path)
		}()
	}
	s.Write(filepath.Join(*output, "SHA256SUMS"), strings.Join(checksums, "\n")+"\n")
}
func writeArchive(path string, binary []byte) {
	file, err := os.Create(path)
	s.Must(err)
	defer file.Close()
	compressed := gzip.NewWriter(file)
	defer compressed.Close()
	archive := tar.NewWriter(compressed)
	defer archive.Close()
	s.Must(archive.WriteHeader(&tar.Header{Name: "koala", Size: int64(len(binary)), Mode: 0755, Format: tar.FormatUSTAR}))
	_, err = archive.Write(binary)
	s.Must(err)
	s.Must(archive.Close())
	s.Must(compressed.Close())
	s.Must(file.Close())
}
