package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/bingyuanng/koala/config"
)

func configEnvJSONCommand(out io.Writer) error {
	data, err := environmentJSON(os.Environ())
	if err != nil {
		return err
	}
	_, err = out.Write(data)
	return err
}

// Convert only supplied variables. Schema information preserves framework scalar
// types without adding defaults; custom scalar values remain strings.
func environmentJSON(environment []string) ([]byte, error) {
	type entry struct {
		key  string
		path []string
		raw  string
	}
	var entries []entry
	paths := map[string]bool{}
	identifier := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	for _, text := range environment {
		if !strings.HasPrefix(text, "KOALA_") {
			continue
		}
		key, raw, ok := strings.Cut(text, "=")
		if !ok {
			return nil, fmt.Errorf("invalid environment setting %s", key)
		}
		path := strings.Split(strings.TrimPrefix(key, "KOALA_"), "__")
		for i, part := range path {
			if !identifier.MatchString(part) {
				return nil, fmt.Errorf("invalid environment path %s", key)
			}
			path[i] = strings.ToLower(part)
		}
		canonical := strings.Join(path, "__")
		if paths[canonical] {
			return nil, fmt.Errorf("duplicate environment path %s", key)
		}
		paths[canonical] = true
		entries = append(entries, entry{key, path, raw})
	}
	// Reject parent/child overrides even if a parent's value is itself an object.
	for _, entry := range entries {
		for i := 1; i < len(entry.path); i++ {
			if paths[strings.Join(entry.path[:i], "__")] {
				return nil, fmt.Errorf("overlapping environment setting %s", entry.key)
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	result := map[string]any{}
	for _, entry := range entries {
		value, err := environmentValue(entry.raw, frameworkConfigType(entry.path))
		if err != nil {
			return nil, fmt.Errorf("invalid value type for %s", entry.key)
		}
		target := result
		for _, part := range entry.path[:len(entry.path)-1] {
			child, ok := target[part].(map[string]any)
			if !ok {
				child = map[string]any{}
				target[part] = child
			}
			target = child
		}
		target[entry.path[len(entry.path)-1]] = value
	}
	data, err := json.MarshalIndent(result, "", "  ")
	return append(data, '\n'), err
}

func frameworkConfigType(path []string) reflect.Type {
	fieldType := reflect.TypeOf(config.Config{})
	for _, part := range path {
		if fieldType.Kind() != reflect.Struct {
			return nil
		}
		var next reflect.Type
		for i := 0; i < fieldType.NumField(); i++ {
			field := fieldType.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" {
				name = field.Name
			}
			if strings.EqualFold(name, part) {
				next = field.Type
				break
			}
		}
		if next == nil {
			return nil
		}
		fieldType = next
	}
	return fieldType
}

func environmentValue(raw string, schema reflect.Type) (any, error) {
	if schema != nil && schema.Kind() == reflect.String {
		return raw, nil
	}
	if schema == nil && !strings.HasPrefix(strings.TrimSpace(raw), "{") && !strings.HasPrefix(strings.TrimSpace(raw), "[") {
		return raw, nil
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	value, err := readConfigValue(decoder)
	if err == nil {
		if _, tailErr := decoder.Token(); tailErr != io.EOF {
			err = fmt.Errorf("invalid JSON value")
		}
	}
	if schema == nil {
		// Without an application schema, retain scalar text and malformed JSON as-is.
		if err != nil {
			return raw, nil
		}
		return value, nil
	}
	if err != nil {
		return nil, err
	}
	// Validate supplied framework types without marshaling zero-valued fields back
	// into the output. Only the original variable's parsed value is emitted.
	if err := json.Unmarshal([]byte(raw), reflect.New(schema).Interface()); err != nil {
		return nil, err
	}
	return value, nil
}
