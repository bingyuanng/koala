package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bingyuanng/koala/internal/scaffold"
)

func writeFiles(root string, files []scaffold.File) error {
	// Preflight all outputs, including existing symlink ancestors, before writing.
	for _, file := range files {
		target := filepath.Join(root, file.Path)
		if _, e := os.Lstat(target); e == nil {
			return fmt.Errorf("refusing to overwrite %s", file.Path)
		} else if !os.IsNotExist(e) {
			return e
		}
		for d := filepath.Dir(target); d != filepath.Clean(root) && d != "." && d != "/"; d = filepath.Dir(d) {
			if info, e := os.Lstat(d); e == nil && info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing symlink ancestor for %s", file.Path)
			}
		}
	}
	created := []string{}
	rollback := func() {
		for _, p := range created {
			_ = os.Remove(p)
		}
	}
	for _, file := range files {
		target := filepath.Join(root, file.Path)
		if e := os.MkdirAll(filepath.Dir(target), 0755); e != nil {
			rollback()
			return e
		}
		f, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if e != nil {
			rollback()
			return e
		}
		created = append(created, target)
		_, e = f.WriteString(file.Content)
		e = errors.Join(e, f.Close())
		if e != nil {
			rollback()
			return e
		}
	}
	return nil
}
