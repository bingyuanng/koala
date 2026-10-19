package generate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bingyuanng/koala/tools"
)

const manifestPath = ".koala/generation.json"

type manifest struct {
	Version   int               `json:"version"`
	Contracts string            `json:"contracts"`
	Input     string            `json:"input"`
	Outputs   map[string]string `json:"outputs"`
}

func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func protoSources(root string, c Config) ([]string, error) {
	var files []string
	protoRoot := filepath.Join(root, c.ProtoRoot)
	err := filepath.WalkDir(protoRoot, func(p string, d os.DirEntry, err error) error {
		if p == protoRoot && os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".proto") {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			files = append(files, rel)
		}
		return nil
	})
	return files, err
}

func contractHash(root string, c Config) (string, error) {
	module, e := ApplicationImportPath(root)
	if e != nil {
		return "", e
	}
	sources, e := protoSources(root, c)
	if e != nil {
		return "", e
	}
	files := append([]string{"koala.yaml", "buf.yaml", "buf.lock", "buf.gen.yaml"}, sources...)
	sort.Strings(files)
	h := sha256.New()
	pinned, e := tools.ReadVersions()
	if e != nil {
		return "", e
	}
	versions, e := json.Marshal(pinned)
	if e != nil {
		return "", e
	}
	fmt.Fprintln(h, module, "koala-generator-v2", hash(versions))
	for _, p := range files {
		b, e := os.ReadFile(filepath.Join(root, p))
		if e != nil {
			return "", e
		}
		fmt.Fprintf(h, "%s\x00%s\x00", p, hash(b))
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func inputHash(ctx context.Context, root string, c Config, services []service) (string, error) {
	contracts, e := contractHash(root, c)
	if e != nil {
		return "", e
	}
	h := sha256.New()
	fmt.Fprintln(h, contracts)
	factories, e := discover(ctx, root, c, services)
	if e != nil {
		return "", e
	}
	names := make([]string, 0, len(factories))
	for k := range factories {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		f := factories[n]
		fmt.Fprintf(h, "%s:%s:%s:%s\n", n, f.Import, f.Name, f.Signature)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CheckFresh excludes business method bodies. Factory annotations/signatures,
// protobuf contracts, configurations and generated output bytes are checked.
func CheckFresh(ctx context.Context, root string) error {
	c, e := readConfig(root)
	if e != nil {
		return e
	}
	m, e := readManifest(root)
	if e != nil || m.Version != 1 {
		return fmt.Errorf("generated artifacts missing; run koala gen")
	}
	services, e := servicesFromManifest(root)
	if e != nil {
		return e
	}
	input, e := inputHash(ctx, root, c, services)
	if e != nil {
		return fmt.Errorf("registration inputs invalid; run koala gen: %w", e)
	}
	if input != m.Input {
		return fmt.Errorf("generation inputs are stale; run koala gen")
	}
	for p, want := range m.Outputs {
		b, e := os.ReadFile(filepath.Join(root, p))
		if e != nil || hash(b) != want {
			return fmt.Errorf("generated output %s is missing or changed; run koala gen", p)
		}
	}
	return nil
}

// Retain descriptors as an owned generation output for offline freshness checks.
func servicesFromManifest(root string) ([]service, error) {
	b, e := os.ReadFile(filepath.Join(root, ".koala/descriptors.bin"))
	if e != nil {
		return nil, fmt.Errorf("descriptor snapshot missing; run koala gen")
	}
	c, e := readConfig(root)
	if e != nil {
		return nil, e
	}
	return servicesFrom(root, c, b)
}

func readManifest(root string) (manifest, error) {
	var m manifest
	b, e := os.ReadFile(filepath.Join(root, manifestPath))
	if e != nil {
		return m, e
	}
	e = json.Unmarshal(b, &m)
	if e != nil {
		return m, e
	}
	if m.Outputs["internal/bootstrap/services_gen.go"] == "" || m.Outputs[".koala/descriptors.bin"] == "" {
		return manifest{}, fmt.Errorf("incomplete generation ownership manifest")
	}
	for p, want := range m.Outputs {
		if !safeOutput(p) || len(want) != 64 {
			return manifest{}, fmt.Errorf("invalid generation ownership manifest")
		}
	}
	return m, nil
}

// Read and verify each reusable artifact once. A cache miss follows the normal
// generation path, whose ownership checks still reject modified outputs.
func cachedTransports(root string, previous manifest, contracts string) ([]byte, map[string][]byte, bool) {
	if previous.Contracts != contracts || previous.Version != 1 {
		return nil, nil, false
	}
	var descriptors []byte
	outputs := map[string][]byte{}
	for path, want := range previous.Outputs {
		if !strings.HasPrefix(path, "gen/") && path != ".koala/descriptors.bin" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || hash(data) != want {
			return nil, nil, false
		}
		if path == ".koala/descriptors.bin" {
			descriptors = data
		} else {
			outputs[path] = data
		}
	}
	return descriptors, outputs, true
}
