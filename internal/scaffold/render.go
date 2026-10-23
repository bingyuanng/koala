// Package scaffold renders embedded, versioned project assets.
package scaffold

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"go/format"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/bingyuanng/koala/config"
)

//go:embed all:templates
var assets embed.FS

type Data struct {
	ModulePath       string
	ProjectName      string
	FrameworkVersion string
	FrameworkPath    string
}

type File struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Ownership string `json:"ownership"`
}

var (
	modulePathPattern     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]*$`)
	releaseVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?$`)
)

func Render(data Data) ([]File, error) {
	if !modulePathPattern.MatchString(data.ModulePath) || strings.Contains(data.ModulePath, "..") {
		return nil, fmt.Errorf("invalid ModulePath")
	}
	for _, segment := range strings.Split(data.ModulePath, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return nil, fmt.Errorf("invalid ModulePath segment")
		}
	}
	if data.ProjectName == "" {
		return nil, fmt.Errorf("ProjectName is required")
	}
	if !releaseVersionPattern.MatchString(data.FrameworkVersion) {
		return nil, fmt.Errorf("FrameworkVersion must be an explicit release version")
	}
	root, err := fs.Sub(assets, "templates/project")
	if err != nil {
		return nil, err
	}
	files, err := renderFS(root, data)
	if err != nil {
		return files, err
	}
	// Record scaffolded generated files before the first generation. Empty input
	// hashes keep this ownership record stale until koala gen completes.
	files = append(files, File{Path: ".koala/descriptors.bin", Ownership: "generated"})
	outputs := map[string]string{}
	for _, file := range files {
		if file.Ownership == "generated" {
			outputs[file.Path] = fmt.Sprintf("%x", sha256.Sum256([]byte(file.Content)))
		}
	}
	manifest, err := json.MarshalIndent(map[string]any{"version": 1, "contracts": "", "input": "", "outputs": outputs}, "", "  ")
	if err != nil {
		return nil, err
	}
	files = append(files, File{Path: ".koala/generation.json", Content: string(manifest) + "\n", Ownership: "generated"})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// renderFS evaluates both names and .tmpl contents, preserving raw assets and
// dotfiles. It returns all files before any caller writes them to disk.
func renderFS(root fs.FS, data any) ([]File, error) {
	var files []File
	seen := map[string]bool{}
	err := fs.WalkDir(root, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%s: asset must be a regular file", name)
		}
		target, err := execute(name, strings.TrimSuffix(name, ".tmpl"), data)
		if err != nil {
			return err
		}
		if !safePath(target) {
			return fmt.Errorf("%s: unsafe output path %q", name, target)
		}
		if seen[target] {
			return fmt.Errorf("%s: duplicate output path %q", name, target)
		}
		seen[target] = true
		source, err := fs.ReadFile(root, name)
		if err != nil {
			return err
		}
		content := string(source)
		if strings.HasSuffix(name, ".tmpl") {
			content, err = execute(name, content, data)
			if err != nil {
				return err
			}
		}
		if strings.HasSuffix(target, ".go") {
			formatted, err := format.Source([]byte(content))
			if err != nil {
				return fmt.Errorf("%s: format Go: %w", target, err)
			}
			content = string(formatted)
		}
		ownership := "application"
		if (strings.HasPrefix(target, "gen/") && path.Base(target) != ".gitkeep") || target == "internal/bootstrap/services_gen.go" {
			ownership = "generated"
		}
		files = append(files, File{Path: target, Content: content, Ownership: ownership})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func safePath(name string) bool {
	return fs.ValidPath(name) && name != "." && path.Clean(name) == name && !strings.ContainsAny(name, "\\:\x00")
}

func execute(name, source string, data any) (string, error) {
	t, err := template.New(name).Funcs(template.FuncMap{"frameworkDefaults": func(name string) (string, error) {
		c := config.DefaultConfig()
		c.App.Name = name
		b, e := json.MarshalIndent(c, "", "  ")
		return string(b), e
	}}).Option("missingkey=error").Parse(source)
	if err != nil {
		return "", fmt.Errorf("%s: parse template: %w", name, err)
	}
	var out bytes.Buffer
	if err := t.Execute(&out, data); err != nil {
		return "", fmt.Errorf("%s: render template: %w", name, err)
	}
	return out.String(), nil
}
