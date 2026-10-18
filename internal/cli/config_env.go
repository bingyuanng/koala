package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/bingyuanng/koala/config"
	"github.com/bingyuanng/koala/internal/generate"
)

func configEnvCommand(args []string, out io.Writer) error {
	if len(args) != 1 || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`).MatchString(args[0]) {
		return errors.New("usage: koala config env <environment> (for example: koala config env prod)")
	}
	root, err := generate.Find(".")
	if err != nil {
		return err
	}
	name := "config." + args[0] + ".json"
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return fmt.Errorf("cannot read %s", name)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := readConfigValue(decoder)
	values, object := value.(map[string]any)
	if err != nil || !object {
		return fmt.Errorf("%s must contain a JSON object with unique keys and no null values", name)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("%s contains invalid or trailing JSON", name)
	}
	lines := []string{"# From " + name, "KOALA_ENV=" + args[0]}
	seen := map[string]bool{"KOALA_ENV": true}
	if err := flattenConfigEnv("KOALA_", values, reflect.TypeOf(config.Config{}), seen, &lines); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, strings.Join(lines, "\n"))
	return err
}

// Read objects without silently replacing duplicate keys, preserving integer precision.
func readConfigValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil || token == nil {
		return nil, errors.New("invalid JSON configuration")
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return token, nil
	}
	switch delim {
	case '{':
		values := map[string]any{}
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := token.(string)
			if !ok {
				return nil, errors.New("invalid JSON object key")
			}
			if _, exists := values[key]; exists {
				return nil, errors.New("duplicate JSON object key")
			}
			value, err := readConfigValue(decoder)
			if err != nil {
				return nil, err
			}
			values[key] = value
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return values, nil
	case '[':
		values := []any{}
		for decoder.More() {
			value, err := readConfigValue(decoder)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return values, nil
	}
	return nil, errors.New("invalid JSON configuration")
}

// Known framework structs use nested keys. Maps, arrays, and application objects
// remain JSON values so conversion does not guess application configuration types.
func flattenConfigEnv(prefix string, values map[string]any, schema reflect.Type, seen map[string]bool, lines *[]string) error {
	fields := map[string]reflect.Type{}
	if schema != nil && schema.Kind() == reflect.Struct {
		for i := 0; i < schema.NumField(); i++ {
			field := schema.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" {
				name = field.Name
			}
			fields[name] = field.Type
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(name) || strings.Contains(name, "__") {
			return errors.New("configuration keys must form unambiguous shell environment names")
		}
		key := prefix + strings.ToUpper(name)
		if seen[key] {
			return fmt.Errorf("configuration keys collide at %s", key)
		}
		// Reserve parent keys too, so differently cased objects cannot overlap.
		seen[key] = true
		value := values[name]
		field := fields[name]
		if object, ok := value.(map[string]any); ok && len(object) > 0 && field != nil && field.Kind() == reflect.Struct {
			if err := flattenConfigEnv(key+"__", object, field, seen, lines); err != nil {
				return err
			}
			continue
		}
		text, stringValue := value.(string)
		if !stringValue {
			data, err := json.Marshal(value)
			if err != nil {
				return errors.New("cannot encode configuration value")
			}
			text = string(data)
		}
		if strings.ContainsRune(text, '\x00') {
			return errors.New("configuration strings cannot contain NUL environment values")
		}
		*lines = append(*lines, key+"='"+strings.ReplaceAll(text, "'", `'\''`)+"'")
	}
	return nil
}
