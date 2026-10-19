package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
	if p == "internal/bootstrap/services_gen.go" || p == ".koala/descriptors.bin" || p == "gen/openapi/api.swagger.json" || p == "gen/openapi/openapi_gen.go" {
		return true
	}
	return strings.HasPrefix(p, "gen/go/") && (strings.HasSuffix(p, ".pb.go") || strings.HasSuffix(p, ".pb.gw.go")) && !strings.Contains(p, "../")
}
