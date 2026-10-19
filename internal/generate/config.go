// Package generate orchestrates Buf and static application registration.
package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Version 1 uses one project layout. These are conventions, not user settings.
const (
	protoDirectory  = "proto"
	servicePackages = "./internal/service/..."
	registryPackage = "bootstrap"
	registryOutput  = "internal/bootstrap/services_gen.go"
)

type Config struct {
	Version        string   `yaml:"version"`
	DependencyType string   `yaml:"dependency_type"`
	Exclude        []string `yaml:"exclude"`
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
	if c.Version != "v1" {
		return c, fmt.Errorf("koala.yaml version must be v1")
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
