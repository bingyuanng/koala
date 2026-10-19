// Package generate orchestrates Buf and static application registration.
package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Version         string   `yaml:"version"`
	ProtoRoot       string   `yaml:"proto_root"`
	ServicePackages string   `yaml:"service_packages"`
	DependencyType  string   `yaml:"dependency_type"`
	Exclude         []string `yaml:"exclude"`
	Registry        struct {
		Package string `yaml:"package"`
		Output  string `yaml:"output"`
	} `yaml:"registry"`
}

func Find(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, e := os.Stat(filepath.Join(dir, "koala.yaml")); e == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("koala.yaml not found in this directory or its ancestors")
		}
		dir = parent
	}
}

func readConfig(root string) (Config, error) {
	var c Config
	f, e := os.Open(filepath.Join(root, "koala.yaml"))
	if e != nil {
		return c, e
	}
	defer f.Close()
	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	if e = decoder.Decode(&c); e != nil {
		return c, fmt.Errorf("invalid koala.yaml: %w", e)
	}
	if c.Version != "v1" || c.ProtoRoot != "proto" || c.ServicePackages != "./internal/service/..." || c.Registry.Package != "bootstrap" || c.Registry.Output != "internal/bootstrap/services_gen.go" {
		return c, fmt.Errorf("koala.yaml v1 requires proto/, ./internal/service/..., and internal/bootstrap/services_gen.go in package bootstrap")
	}
	if _, _, e = splitType(c.DependencyType); e != nil {
		return c, e
	}
	return c, nil
}

func splitType(s string) (string, string, error) {
	i := strings.LastIndex(s, ".")
	if i < 1 || i == len(s)-1 {
		return "", "", fmt.Errorf("dependency_type must be a qualified Go import path and type")
	}
	return s[:i], s[i+1:], nil
}
