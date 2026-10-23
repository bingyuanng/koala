// Package safefile reads configuration material without disclosing paths or contents in errors.
package safefile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// ReadFile reports a fixed description and safe error category. Description must
// be a constant label, not a path or user-supplied value.
func ReadFile(path, description string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, readError(description, err)
	}
	return data, nil
}

func readError(description string, err error) error {
	for _, category := range []error{fs.ErrNotExist, fs.ErrPermission} {
		if errors.Is(err, category) {
			return fmt.Errorf("%s: %w", description, category)
		}
	}
	return fmt.Errorf("%s: file I/O error", description)
}
