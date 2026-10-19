package config

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

type fieldPath struct {
	names  []string
	path   string
	schema *configFields
	quoted bool
}

type configField struct {
	index  int
	schema *configFields
}

// One type walk supplies both environment paths and JSON merge rules.
type configFields struct {
	typ     reflect.Type
	kind    reflect.Kind
	fields  map[string]configField
	element *configFields
}

func indexFields(fieldType reflect.Type, names []string, prefix string, index map[string]fieldPath, seen map[reflect.Type]bool) (*configFields, error) {
	schema := &configFields{typ: fieldType}
	for fieldType.Kind() == reflect.Pointer {
		fieldType = fieldType.Elem()
	}
	schema.kind = fieldType.Kind()
	if reflect.PointerTo(fieldType).Implements(reflect.TypeFor[json.Unmarshaler]()) || reflect.PointerTo(fieldType).Implements(reflect.TypeFor[encoding.TextUnmarshaler]()) {
		return schema, nil
	}
	if seen[fieldType] {
		return nil, fmt.Errorf("config: recursive configuration types are unsupported")
	}
	seen[fieldType] = true
	defer delete(seen, fieldType)
	switch fieldType.Kind() {
	case reflect.Map, reflect.Slice, reflect.Array:
		// Container values use the same JSON rules, but do not create environment paths.
		var err error
		schema.element, err = indexFields(fieldType.Elem(), names, prefix, nil, seen)
		return schema, err
	case reflect.Struct:
	default:
		return schema, nil
	}
	schema.fields = map[string]configField{}
	for i := 0; i < fieldType.NumField(); i++ {
		structField := fieldType.Field(i)
		if !structField.IsExported() {
			continue
		}
		tag := strings.Split(structField.Tag.Get("json"), ",")
		name := tag[0]
		if name == "-" {
			continue
		}
		if structField.Anonymous && name == "" {
			return nil, fmt.Errorf("config: anonymous fields require a named configuration section")
		}
		if name == "" {
			name = structField.Name
		}
		if strings.Contains(name, "__") || strings.ContainsAny(name, ".=") {
			return nil, fmt.Errorf("config: ambiguous JSON field name")
		}
		for existing := range schema.fields {
			if strings.EqualFold(existing, name) {
				return nil, fmt.Errorf("config: JSON field name collision at %s", name)
			}
		}
		path := append(append([]string{}, names...), name)
		key := prefix + strings.ToUpper(strings.Join(path, "__"))
		if _, exists := index[key]; exists {
			return nil, fmt.Errorf("config: environment path collision at %s", key)
		}
		child, err := indexFields(structField.Type, path, prefix, index, seen)
		if err != nil {
			return nil, err
		}
		quoted := false
		for _, option := range tag[1:] {
			quoted = quoted || option == "string"
		}
		switch child.kind {
		case reflect.Bool, reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr, reflect.Float32, reflect.Float64:
		default:
			quoted = false // encoding/json ignores ,string on aggregate types.
		}
		if index != nil {
			index[key] = fieldPath{names: path, path: strings.Join(path, "."), schema: child, quoted: quoted}
		}
		schema.fields[name] = configField{index: i, schema: child}
	}
	return schema, nil
}

func (schema *configFields) field(name string) (string, configField, bool) {
	if field, ok := schema.fields[name]; ok {
		return name, field, true
	}
	for canonical, field := range schema.fields {
		if strings.EqualFold(canonical, name) {
			return canonical, field, true
		}
	}
	return "", configField{}, false
}

func (schema *configFields) merge(base, source any, path string, suppliedPaths map[string]bool) (any, error) {
	if schema.element != nil {
		// Replace the container, while normalizing struct fields inside its values.
		suppliedPaths[path] = true
		switch source := source.(type) {
		case map[string]any:
			for key, value := range source {
				var err error
				source[key], err = schema.element.merge(nil, value, path+"."+key, suppliedPaths)
				if err != nil {
					return nil, err
				}
			}
			return source, nil
		case []any:
			for i, value := range source {
				var err error
				source[i], err = schema.element.merge(nil, value, path, suppliedPaths)
				if err != nil {
					return nil, err
				}
			}
			return source, nil
		}
	}
	object, isObject := source.(map[string]any)
	if schema.fields == nil || !isObject {
		markPresent(source, path, suppliedPaths)
		return source, nil // Maps, arrays and scalars replace their previous value.
	}
	merged, _ := base.(map[string]any)
	if merged == nil {
		merged = map[string]any{}
	}
	suppliedPaths[path] = true
	seen := map[string]bool{}
	for name, value := range object {
		canonical, field, ok := schema.field(name)
		if !ok {
			return nil, fmt.Errorf("config: unknown JSON field %q", name)
		}
		child := canonical
		if path != "" {
			child = path + "." + canonical
		}
		if seen[canonical] {
			return nil, fmt.Errorf("config: duplicate JSON setting at %s", child)
		}
		seen[canonical] = true
		var err error
		merged[canonical], err = field.schema.merge(merged[canonical], value, child, suppliedPaths)
		if err != nil {
			return nil, err
		}
	}
	return merged, nil
}

func markPresent(value any, path string, suppliedPaths map[string]bool) {
	suppliedPaths[path] = true
	switch value := value.(type) {
	case map[string]any:
		for name, child := range value {
			childPath := name
			if path != "" {
				childPath = path + "." + name
			}
			markPresent(child, childPath, suppliedPaths)
		}
	case []any:
		for _, child := range value {
			markPresent(child, path, suppliedPaths)
		}
	}
}

// JSON cannot represent unexported or json:"-" defaults. Retain those fields
// from the constructor while taking every supplied JSON field from the fresh
// typed result. No JSON matching or aggregate resetting is needed here.
func (schema *configFields) preserveDefaults(defaults, decoded reflect.Value, path string, suppliedPaths map[string]bool) reflect.Value {
	if !suppliedPaths[path] {
		return defaults
	}
	if schema.fields == nil {
		return decoded
	}
	var pointerTypes []reflect.Type
	for decoded.Kind() == reflect.Pointer {
		if decoded.IsNil() {
			return decoded
		}
		pointerTypes = append(pointerTypes, decoded.Type())
		decoded = decoded.Elem()
		if defaults.IsNil() {
			defaults = reflect.Zero(defaults.Type().Elem())
		} else {
			defaults = defaults.Elem()
		}
	}
	merged := reflect.New(decoded.Type()).Elem()
	merged.Set(defaults)
	for name, field := range schema.fields {
		child := name
		if path != "" {
			child = path + "." + name
		}
		merged.Field(field.index).Set(field.schema.preserveDefaults(defaults.Field(field.index), decoded.Field(field.index), child, suppliedPaths))
	}
	for i := len(pointerTypes) - 1; i >= 0; i-- {
		pointer := reflect.New(merged.Type())
		pointer.Elem().Set(merged)
		merged = pointer.Convert(pointerTypes[i])
	}
	return merged
}
