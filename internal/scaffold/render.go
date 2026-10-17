// Package scaffold renders embedded, versioned project and service assets.
package scaffold

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"github.com/bingyuanng/koala/config"
	"go/format"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

//go:embed all:templates
var assets embed.FS

type Data struct {
	ModulePath       string
	ProjectName      string
	ServiceName      string
	FrameworkVersion string
	// ServiceDirectory is derived from ServiceName by Render.
	ServiceDirectory string
}

type File struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Ownership string `json:"ownership"`
}

func Render(kind string, data Data) ([]File, error) {
	if kind != "project" && kind != "service" {
		return nil, fmt.Errorf("unknown template set %q", kind)
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]*$`).MatchString(data.ModulePath) || strings.Contains(data.ModulePath, "..") {
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
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(data.ServiceName) {
		return nil, fmt.Errorf("ServiceName must be a Go identifier")
	}
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?$`).MatchString(data.FrameworkVersion) {
		return nil, fmt.Errorf("FrameworkVersion must be an explicit release version")
	}
	data.ServiceDirectory = strings.ToLower(strings.TrimSuffix(data.ServiceName, "Service"))
	if data.ServiceDirectory == "" {
		return nil, fmt.Errorf("ServiceName must include a domain name")
	}
	root, err := fs.Sub(assets, "templates/"+kind)
	if err != nil {
		return nil, err
	}
	return renderFS(root, data)
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
		if strings.HasPrefix(target, "gen/") {
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
	}, "json": func(value any) (string, error) {
		encoded, err := json.Marshal(value)
		return string(encoded), err
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
