package config

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
)

// Options constructs a fresh application configuration. Required lists JSON
// paths that must be explicitly supplied by a source, even for zero/false values.
// Validate must return secret-safe diagnostics. Env nil means os.Environ().
// For LoadWithOptions, Environment defaults to the prefix's ENV setting, then dev. Only dev reads File.
type Options[T any] struct {
	Defaults    func() T
	File        string
	Environment string
	Prefix      string
	Env         []string
	Required    []string
	Validate    func(T) error
}

// Load merges defaults, config.dev.json only in dev, and KOALA_ environment
// overrides, then validates. KOALA_ENV defaults to dev; every other environment
// uses defaults and environment overrides without reading a file.
// Use LoadWithOptions to select other sources or require explicit settings.
func Load[T any](defaults func() T, validate func(T) error) (T, error) {
	return LoadWithOptions(Options[T]{Defaults: defaults, Validate: validate})
}

// LoadWithOptions loads and validates configuration from explicitly selected sources.
func LoadWithOptions[T any](o Options[T]) (T, error) {
	o = resolveSources(o)
	environment := o.Environment
	for _, e := range o.Env {
		k, v, ok := strings.Cut(e, "=")
		if ok && k == o.Prefix+"ENV" && environment == "" {
			environment = v
		}
	}
	return load(o, environment == "" || environment == "dev")
}

// LoadFile merges defaults, the named JSON file, and environment overrides.
// The caller selects the file explicitly; Environment and KOALA_ENV do not select sources.
func LoadFile[T any](file string, o Options[T]) (T, error) {
	if file == "" {
		var zero T
		return zero, fmt.Errorf("config: configuration file path is required")
	}
	o.File = file
	return load(resolveSources(o), true)
}

// LoadEnv merges defaults and environment overrides without reading a file.
func LoadEnv[T any](o Options[T]) (T, error) {
	return load(resolveSources(o), false)
}

func resolveSources[T any](o Options[T]) Options[T] {
	if o.Prefix == "" {
		o.Prefix = "KOALA_"
	}
	if o.Env == nil {
		o.Env = os.Environ()
	}
	return o
}

func load[T any](o Options[T], readFile bool) (T, error) {
	var zero T
	cfg := zero
	if o.Defaults != nil {
		cfg = o.Defaults()
	}
	value := reflect.ValueOf(&cfg).Elem()
	if value.Kind() != reflect.Struct {
		return zero, fmt.Errorf("config: destination must be a struct")
	}
	prefix, env := o.Prefix, o.Env
	index := map[string]fieldPath{}
	schema, err := indexFields(value.Type(), nil, prefix, index, map[reflect.Type]bool{})
	if err != nil {
		return zero, err
	}
	if _, ok := index[prefix+"ENV"]; ok {
		return zero, fmt.Errorf("config: field collides with reserved environment key")
	}
	tree, err := defaultsTree(cfg)
	if err != nil {
		return zero, err
	}
	present := map[string]bool{}
	if readFile {
		file := o.File
		if file == "" {
			file = "config.dev.json"
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return zero, fmt.Errorf("config: cannot read configuration file")
		}
		source, err := parseJSON(raw, "")
		if err != nil {
			return zero, err
		}
		if err := decodeTree(source, reflect.New(value.Type()).Interface(), ""); err != nil {
			return zero, err
		}
		tree, err = schema.merge(tree, source, "", present)
		if err != nil {
			return zero, err
		}
	}
	overrides := map[string]string{}
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if !ok || !strings.HasPrefix(k, prefix) || k == prefix+"ENV" {
			continue
		}
		if _, ok = index[k]; !ok {
			return zero, fmt.Errorf("config: unknown environment setting %s", k)
		}
		if _, ok = overrides[k]; ok {
			return zero, fmt.Errorf("config: duplicate environment setting %s", k)
		}
		overrides[k] = v
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		childPrefix := k + "__"
		i := sort.SearchStrings(keys, childPrefix)
		if i < len(keys) && strings.HasPrefix(keys[i], childPrefix) {
			return zero, fmt.Errorf("config: overlapping settings %s and %s", k, keys[i])
		}
	}
	for _, k := range keys {
		f := index[k]
		var source any = overrides[k]
		if f.schema.kind != reflect.String {
			source, err = parseJSON([]byte(overrides[k]), f.path)
			if err != nil {
				return zero, fmt.Errorf("config: %s expects %s", f.path, f.schema.typ)
			}
		}
		// Validate each source before merging so an override cannot hide invalid input.
		if err := decodeTree(source, reflect.New(f.schema.typ).Interface(), f.path); err != nil {
			return zero, fmt.Errorf("config: %s expects %s", f.path, f.schema.typ)
		}
		if f.quoted {
			encoded, err := json.Marshal(source)
			if err != nil {
				return zero, fmt.Errorf("config: cannot encode %s", f.path)
			}
			source = string(encoded)
		}
		for i := len(f.names) - 1; i >= 0; i-- {
			source = map[string]any{f.names[i]: source}
		}
		tree, err = schema.merge(tree, source, "", present)
		if err != nil {
			return zero, err
		}
	}
	var resolved T
	if err := decodeTree(tree, &resolved, ""); err != nil {
		return zero, err
	}
	value.Set(schema.preserveDefaults(value, reflect.ValueOf(resolved), "", present))
	for _, path := range o.Required {
		if !present[path] {
			return zero, fmt.Errorf("config: required setting %s is missing", path)
		}
	}
	if o.Validate != nil {
		if err := o.Validate(cfg); err != nil {
			return zero, err
		}
	}
	return cfg, nil
}
