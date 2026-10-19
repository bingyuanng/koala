package generate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type outputState struct {
	data   []byte
	mode   os.FileMode
	exists bool
}

// Generated transports must be available to Go's type checker before registry
// validation. Keep their original bytes until every generation stage succeeds.
type outputTransaction struct {
	committed bool
	root      string
	originals map[string]outputState
	expected  map[string]outputState
	paths     []string
}

func newOutputTransaction(root string) *outputTransaction {
	return &outputTransaction{root: root, originals: map[string]outputState{}, expected: map[string]outputState{}}
}

func readOutput(path string) (outputState, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return outputState{}, nil
	}
	if err != nil {
		return outputState{}, err
	}
	if !info.Mode().IsRegular() {
		return outputState{}, fmt.Errorf("refusing non-regular output %s", path)
	}
	data, err := os.ReadFile(path)
	return outputState{data: data, mode: info.Mode().Perm(), exists: true}, err
}

func (t *outputTransaction) record(path string) error {
	if _, exists := t.originals[path]; exists {
		return nil
	}
	before, err := readOutput(filepath.Join(t.root, path))
	if err != nil {
		return err
	}
	t.originals[path] = before
	t.paths = append(t.paths, path)
	return nil
}

func (t *outputTransaction) write(path string, data []byte) error {
	if err := t.record(path); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(t.root, path), data); err != nil {
		return err
	}
	t.expected[path] = outputState{data: append([]byte(nil), data...), mode: 0644, exists: true}
	return nil
}

func checkOwned(root, path string, data []byte, previous manifest) error {
	old, err := readOutput(filepath.Join(root, path))
	if err != nil || !old.exists {
		return err
	}
	// The manifest establishes ownership. Hashes detect stale outputs for start
	// and cache reuse; regeneration intentionally replaces edits to owned files.
	if _, owned := previous.Outputs[path]; !owned && !bytes.Equal(old.data, data) {
		return fmt.Errorf("refusing to overwrite unowned output %s", path)
	}
	return nil
}

// checkWrites preflights the complete batch before any file is installed.
func (t *outputTransaction) checkWrites(outputs map[string][]byte, previous manifest) ([]string, error) {
	paths := make([]string, 0, len(outputs))
	for path := range outputs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := checkOwned(t.root, path, outputs[path], previous); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

// writeOutputs installs registration files without removing transport outputs.
func (t *outputTransaction) writeOutputs(outputs map[string][]byte, previous manifest) error {
	paths, err := t.checkWrites(outputs, previous)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := t.write(path, outputs[path]); err != nil {
			return err
		}
	}
	return nil
}

// commit publishes ownership/freshness only after consumer validation succeeds.
func (t *outputTransaction) commit(contracts, input string, outputs map[string][]byte) error {
	m := manifest{Version: 1, Contracts: contracts, Input: input, Outputs: map[string]string{}}
	for path, data := range outputs {
		m.Outputs[path] = hash(data)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := t.write(manifestPath, append(data, '\n')); err != nil {
		return err
	}
	t.committed = true
	return nil
}

func (t *outputTransaction) install(outputs map[string][]byte, previous manifest) error {
	writes, err := t.checkWrites(outputs, previous)
	if err != nil {
		return err
	}
	var removals []string
	for path := range previous.Outputs {
		if _, retained := outputs[path]; retained || !strings.HasPrefix(path, "gen/") {
			continue
		}
		old, err := readOutput(filepath.Join(t.root, path))
		if err != nil {
			return err
		}
		if !old.exists {
			continue
		}
		removals = append(removals, path)
	}
	sort.Strings(removals)
	for _, path := range writes {
		if err := t.write(path, outputs[path]); err != nil {
			return err
		}
	}
	for _, path := range removals {
		if err := t.record(path); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(t.root, path)); err != nil {
			return err
		}
		t.expected[path] = outputState{}
	}
	return nil
}

func (t *outputTransaction) rollback() error {
	if t.committed {
		return nil
	}
	var result error
	for i := len(t.paths) - 1; i >= 0; i-- {
		path := t.paths[i]
		expected, changed := t.expected[path]
		if !changed {
			continue
		}
		dest := filepath.Join(t.root, path)
		current, err := readOutput(dest)
		if err == nil && (current.exists != expected.exists || current.mode != expected.mode || !bytes.Equal(current.data, expected.data)) {
			err = fmt.Errorf("output changed during generation; refusing rollback of %s", path)
		}
		if err == nil {
			original := t.originals[path]
			if original.exists {
				err = atomicWrite(dest, original.data)
				if err == nil {
					err = os.Chmod(dest, original.mode)
				}
			} else if current.exists {
				err = os.Remove(dest)
			}
		}
		if err != nil {
			result = errors.Join(result, fmt.Errorf("generation rollback for %s: %w", path, err))
		}
	}
	return result
}

func generatedFiles(root string) (map[string][]byte, error) {
	files := map[string][]byte{}
	e := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(root, p)
		target := "gen/" + filepath.ToSlash(rel)
		if !safeOutput(target) {
			return fmt.Errorf("unexpected generated output %s", target)
		}
		files[target] = b
		return nil
	})
	return files, e
}

func atomicWrite(path string, b []byte) error {
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".koala-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Chmod(0644); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(name, path)
}

func rejectSymlinks(root string, paths []string) error {
	for _, p := range paths {
		full := filepath.Join(root, p)
		for d := full; d != root && strings.HasPrefix(d, root+string(os.PathSeparator)); d = filepath.Dir(d) {
			if info, e := os.Lstat(d); e == nil && info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing symlink path %s", p)
			}
		}
		e := filepath.WalkDir(full, func(path string, d os.DirEntry, e error) error {
			if os.IsNotExist(e) {
				return nil
			}
			if e != nil {
				return e
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing symlink %s", path)
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	return nil
}

func safeOutput(p string) bool {
	if filepath.ToSlash(filepath.Clean(p)) != p || strings.Contains(p, "\\") || filepath.IsAbs(p) {
		return false
	}
	if p == registryOutput || p == ".koala/descriptors.bin" || p == "gen/openapi/api.swagger.json" || p == "gen/openapi/openapi_gen.go" {
		return true
	}
	return strings.HasPrefix(p, "gen/go/") && (strings.HasSuffix(p, ".pb.go") || strings.HasSuffix(p, ".pb.gw.go")) && !strings.Contains(p, "../")
}
