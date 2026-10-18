// Package scriptutil contains helpers for repository maintenance commands.
package scriptutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
)

func Must(err error) {
	if err != nil {
		panic(err)
	}
}
func Check(ok bool, message string, args ...any) {
	if !ok {
		panic(fmt.Sprintf(message, args...))
	}
}
func Main(fn func()) {
	defer func() {
		if err := recover(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}()
	fn()
}
func Root() string {
	_, file, _, ok := runtime.Caller(0)
	Check(ok, "cannot locate script source")
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
}
func Temp(prefix string) string { dir, err := os.MkdirTemp("", prefix); Must(err); return dir }
func Read(path string) string   { data, err := os.ReadFile(path); Must(err); return string(data) }
func Write(path, data string) {
	Must(os.MkdirAll(filepath.Dir(path), 0755))
	Must(os.WriteFile(path, []byte(data), 0644))
}
func Remove(path string) { Must(os.Remove(path)) }
func Exists(path string) bool {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	Must(err)
	return true
}
func Run(dir string, env []string, failure bool, args ...string) string {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if failure {
		Check(err != nil, "%v unexpectedly succeeded:\n%s", args, output)
		var exit *exec.ExitError
		Check(errors.As(err, &exit), "%v could not start: %v", args, err)
	} else {
		Check(err == nil, "%v failed: %v\n%s", args, err, output)
	}
	return string(output)
}
func Snapshot(dir string) map[string]string {
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) && path == dir {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(rel)] = Read(path)
		}
		return nil
	})
	Must(err)
	return files
}
func Compare(a, b any, description string) {
	if reflect.DeepEqual(a, b) {
		return
	}
	expected, expectedOK := a.(map[string]string)
	actual, actualOK := b.(map[string]string)
	if expectedOK && actualOK {
		differences := []string{}
		for name, value := range expected {
			if other, ok := actual[name]; !ok || other != value {
				differences = append(differences, name)
			}
		}
		for name := range actual {
			if _, ok := expected[name]; !ok {
				differences = append(differences, name)
			}
		}
		sort.Strings(differences)
		panic(description + ":\n" + strings.Join(differences, "\n"))
	}
	panic(description)
}
func Contains(value, part string) {
	Check(strings.Contains(value, part), "expected %q in:\n%s", part, value)
}
func NotContains(value, part string) {
	Check(!strings.Contains(value, part), "unexpected %q in:\n%s", part, value)
}
func Env(values map[string]string, removePrefix string) []string {
	env := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if _, replace := values[key]; !replace && (removePrefix == "" || !strings.HasPrefix(key, removePrefix)) {
			env = append(env, value)
		}
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}
