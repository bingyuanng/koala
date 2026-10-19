package generate

import (
	"bytes"
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
	if oldHash, owned := previous.Outputs[path]; owned {
		if hash(old.data) != oldHash {
			return fmt.Errorf("refusing to overwrite modified generated output %s", path)
		}
	} else if strings.HasSuffix(path, ".go") && !bytes.HasPrefix(old.data, []byte("// Code generated")) {
		return fmt.Errorf("refusing to overwrite application source %s", path)
	} else if strings.HasSuffix(path, ".json") && !bytes.Equal(old.data, data) {
		return fmt.Errorf("unowned output %s", path)
	}
	return nil
}

func (t *outputTransaction) writeOwned(path string, data []byte, previous manifest) error {
	if err := checkOwned(t.root, path, data, previous); err != nil {
		return err
	}
	return t.write(path, data)
}

func (t *outputTransaction) install(outputs map[string][]byte, previous manifest) error {
	var writes, removals []string
	// Check every ownership conflict before changing any transport file.
	for path, data := range outputs {
		if err := checkOwned(t.root, path, data, previous); err != nil {
			return err
		}
		writes = append(writes, path)
	}
	for path, want := range previous.Outputs {
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
		if hash(old.data) != want {
			return fmt.Errorf("refusing to remove modified generated output %s", path)
		}
		removals = append(removals, path)
	}
	sort.Strings(writes)
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
