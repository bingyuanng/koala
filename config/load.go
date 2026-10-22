package config

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/bingyuanng/koala/internal/safefile"
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
func LoadWithOptions[T any](options Options[T]) (T, error) {
	options = resolveSources(options)
	environment := options.Environment
	for _, entry := range options.Env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == options.Prefix+"ENV" && environment == "" {
			environment = value
		}
	}
	return load(options, environment == "" || environment == "dev")
}

// LoadFile merges defaults, the named JSON file, and environment overrides.
// The caller selects the file explicitly; Environment and KOALA_ENV do not select sources.
func LoadFile[T any](file string, options Options[T]) (T, error) {
	if file == "" {
		var zero T
		return zero, fmt.Errorf("config: configuration file path is required")
	}
	options.File = file
	return load(resolveSources(options), true)
}

// LoadEnv merges defaults and environment overrides without reading a file.
func LoadEnv[T any](options Options[T]) (T, error) {
	return load(resolveSources(options), false)
}

func resolveSources[T any](options Options[T]) Options[T] {
	if options.Prefix == "" {
		options.Prefix = "KOALA_"
	}
	if options.Env == nil {
		options.Env = os.Environ()
	}
	return options
}

func load[T any](options Options[T], readFile bool) (T, error) {
	var zero T
	configuration := zero
	if options.Defaults != nil {
		configuration = options.Defaults()
	}
	destination := reflect.ValueOf(&configuration).Elem()
	if destination.Kind() != reflect.Struct {
		return zero, fmt.Errorf("config: destination must be a struct")
	}
	prefix, environment := options.Prefix, options.Env
	fieldsByEnvironment := map[string]fieldPath{}
	schema, err := indexFields(destination.Type(), nil, prefix, fieldsByEnvironment, map[reflect.Type]bool{})
	if err != nil {
		return zero, err
	}
	if _, ok := fieldsByEnvironment[prefix+"ENV"]; ok {
		return zero, fmt.Errorf("config: field collides with reserved environment key")
	}
	merged, err := defaultsTree(configuration)
	if err != nil {
		return zero, err
	}
	suppliedPaths := map[string]bool{}
	if readFile {
		file := options.File
		if file == "" {
			file = "config.dev.json"
		}
		raw, err := safefile.ReadFile(file, "config: cannot read configuration file")
		if err != nil {
			return zero, err
		}
		source, err := parseJSON(raw, "")
		if err != nil {
			return zero, err
		}
		if err := decodeTree(source, reflect.New(destination.Type()).Interface(), ""); err != nil {
			return zero, err
		}
		merged, err = schema.merge(merged, source, "", suppliedPaths)
		if err != nil {
			return zero, err
		}
	}
	values := map[string]string{}
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(key, prefix) || key == prefix+"ENV" {
			continue
		}
		if _, known := fieldsByEnvironment[key]; !known {
			return zero, fmt.Errorf("config: unknown environment setting %s", key)
		}
		if _, duplicate := values[key]; duplicate {
			return zero, fmt.Errorf("config: duplicate environment setting %s", key)
		}
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		childPrefix := key + "__"
		childIndex := sort.SearchStrings(keys, childPrefix)
		if childIndex < len(keys) && strings.HasPrefix(keys[childIndex], childPrefix) {
			return zero, fmt.Errorf("config: overlapping settings %s and %s", key, keys[childIndex])
		}
	}
	for _, key := range keys {
		source, err := environmentSource(fieldsByEnvironment[key], values[key])
		if err != nil {
			return zero, err
		}
		merged, err = schema.merge(merged, source, "", suppliedPaths)
		if err != nil {
			return zero, err
		}
	}
	var resolved T
	if err := decodeTree(merged, &resolved, ""); err != nil {
		return zero, err
	}
	destination.Set(schema.preserveDefaults(destination, reflect.ValueOf(resolved), "", suppliedPaths))
	for _, path := range options.Required {
		if !suppliedPaths[path] {
			return zero, fmt.Errorf("config: required setting %s is missing", path)
		}
	}
	if options.Validate != nil {
		if err := options.Validate(configuration); err != nil {
			return zero, err
		}
	}
	return configuration, nil
}

// environmentSource decodes and type-checks one value, then wraps it in the
// nested JSON field names used by the shared merge rules.
func environmentSource(field fieldPath, raw string) (any, error) {
	var source any = raw
	if field.schema.kind != reflect.String {
		decoded, err := parseJSON([]byte(raw), field.path)
		if err != nil {
			return nil, fmt.Errorf("config: %s expects %s", field.path, field.schema.typ)
		}
		source = decoded
	}
	// Validate each source before merging so later overrides cannot hide bad input.
	if err := decodeTree(source, reflect.New(field.schema.typ).Interface(), field.path); err != nil {
		return nil, fmt.Errorf("config: %s expects %s", field.path, field.schema.typ)
	}
	if field.quoted {
		encoded, err := json.Marshal(source)
		if err != nil {
			return nil, fmt.Errorf("config: cannot encode %s", field.path)
		}
		source = string(encoded)
	}
	for i := len(field.names) - 1; i >= 0; i-- {
		source = map[string]any{field.names[i]: source}
	}
	return source, nil
}
