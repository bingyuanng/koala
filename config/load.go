package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
)

// Options constructs a fresh application configuration. Required lists JSON
// paths that must be explicitly supplied by a source, even for zero/false values.
// Validate must return secret-safe diagnostics. Env nil means os.Environ().
type Options[T any] struct {
	Defaults func() T
	File     string
	Mode     string
	Prefix   string
	Env      []string
	Required []string
	Validate func(T) error
}

func Load[T any](o Options[T]) (T, error) {
	var zero T
	cfg := zero
	if o.Defaults != nil {
		cfg = o.Defaults()
	}
	value := reflect.ValueOf(&cfg).Elem()
	if value.Kind() != reflect.Struct {
		return zero, fmt.Errorf("config: destination must be a struct")
	}
	prefix := o.Prefix
	if prefix == "" {
		prefix = "KOALA_"
	}
	env := o.Env
	if env == nil {
		env = os.Environ()
	}
	mode := o.Mode
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if ok && k == prefix+"ENV" && mode == "" {
			mode = v
		}
	}
	if mode == "" {
		mode = "local"
	}
	if mode != "local" && mode != "cloud" {
		return zero, fmt.Errorf("config: mode must be local or cloud")
	}
	index := map[string]fieldPath{}
	if err := indexFields(value.Type(), nil, nil, prefix, index, map[reflect.Type]bool{}); err != nil {
		return zero, err
	}
	if _, ok := index[prefix+"ENV"]; ok {
		return zero, fmt.Errorf("config: field collides with reserved mode key")
	}
	present := map[string]bool{}
	if mode == "local" {
		file := o.File
		if file == "" {
			file = "config.dev.json"
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return zero, fmt.Errorf("config: cannot read local configuration file")
		}
		if err = decode(b, &cfg, present, ""); err != nil {
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
	for i, k := range keys {
		for _, other := range keys[i+1:] {
			if strings.HasPrefix(other, k+"__") {
				return zero, fmt.Errorf("config: overlapping settings %s and %s", k, other)
			}
		}
	}
	for _, k := range keys {
		f := index[k]
		v := configField(value, f.indices)
		raw := []byte(overrides[k])
		kind := v.Type()
		for kind.Kind() == reflect.Pointer {
			kind = kind.Elem()
		}
		if kind.Kind() == reflect.String {
			raw, _ = json.Marshal(overrides[k])
		}
		// Struct objects retain constructor defaults for omitted members; maps and
		// arrays replace the complete aggregate.
		fresh := reflect.New(v.Type())
		if kind.Kind() == reflect.Struct {
			initial, err := json.Marshal(v.Interface())
			if err != nil {
				return zero, fmt.Errorf("config: cannot copy defaults at %s", f.path)
			}
			if err = json.Unmarshal(initial, fresh.Interface()); err != nil {
				return zero, fmt.Errorf("config: cannot copy defaults at %s", f.path)
			}
		}
		if err := decode(raw, fresh.Interface(), present, f.path); err != nil {
			return zero, fmt.Errorf("config: %s expects %s", f.path, v.Type())
		}
		v.Set(fresh.Elem())
	}
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

type fieldPath struct {
	indices []int
	path    string
}

func indexFields(t reflect.Type, indices []int, names []string, prefix string, index map[string]fieldPath, seen map[reflect.Type]bool) error {
	if seen[t] {
		return fmt.Errorf("config: recursive configuration types are unsupported")
	}
	seen[t] = true
	defer delete(seen, t)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if f.Anonymous && strings.Split(f.Tag.Get("json"), ",")[0] == "" {
			return fmt.Errorf("config: anonymous fields require a named configuration section")
		}
		if strings.Contains(name, "__") || strings.ContainsAny(name, ".=") || name == "" {
			return fmt.Errorf("config: ambiguous JSON field name")
		}
		path := append(append([]string{}, names...), name)
		ix := append(append([]int{}, indices...), i)
		upper := make([]string, len(path))
		for i, n := range path {
			upper[i] = strings.ToUpper(n)
		}
		key := prefix + strings.Join(upper, "__")
		if _, ok := index[key]; ok {
			return fmt.Errorf("config: environment path collision at %s", key)
		}
		index[key] = fieldPath{ix, strings.Join(path, ".")}
		fieldType := f.Type
		for fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}
		if fieldType.Kind() == reflect.Struct {
			if err := indexFields(fieldType, ix, path, prefix, index, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

// decode checks duplicate keys before ordinary strict typed decoding. Neither
// parser errors nor submitted values are included in diagnostics.
func decode(raw []byte, dst any, present map[string]bool, path string) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := scanJSON(d, present, path); err != nil {
		return fmt.Errorf("config: invalid or duplicate JSON at %s", path)
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("config: trailing JSON at %s", path)
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		var mismatch *json.UnmarshalTypeError
		if errors.As(err, &mismatch) {
			field := mismatch.Field
			if path != "" && field != "" {
				field = path + "." + field
			} else if field == "" {
				field = path
			}
			return fmt.Errorf("config: %s expects %s", field, mismatch.Type)
		}
		scope := path
		if scope == "" {
			scope = "configuration"
		}
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			return fmt.Errorf("config: %s: %s", scope, err)
		}
		return fmt.Errorf("config: invalid JSON at %s", scope)
	}
	return nil
}
func scanJSON(d *json.Decoder, present map[string]bool, path string) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return fmt.Errorf("null is not a configuration value")
	}
	present[path] = true
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, err = d.Token()
			if err != nil {
				return err
			}
			key := token.(string)
			if seen[key] {
				return fmt.Errorf("duplicate")
			}
			seen[key] = true
			child := key
			if path != "" {
				child = path + "." + key
			}
			if err = scanJSON(d, present, child); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err = scanJSON(d, present, path); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected delimiter")
	}
	_, err = d.Token()
	return err
}

func configField(value reflect.Value, indices []int) reflect.Value {
	for _, i := range indices {
		for value.Kind() == reflect.Pointer {
			if value.IsNil() {
				value.Set(reflect.New(value.Type().Elem()))
			}
			value = value.Elem()
		}
		value = value.Field(i)
	}
	return value
}
