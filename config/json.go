package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Source JSON is parsed once, before decoding can discard duplicate keys or nulls.
// Number tokens retain their text so large integers do not pass through float64.
func parseJSON(raw []byte, path string) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	value, err := parseValue(d)
	if err != nil {
		return nil, fmt.Errorf("config: invalid or duplicate JSON at %s", path)
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("config: trailing JSON at %s", path)
	}
	return value, nil
}

func parseValue(d *json.Decoder) (any, error) {
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, errors.New("null is not a configuration value")
	}
	switch token {
	case json.Delim('{'):
		object := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			name := key.(string)
			if _, duplicate := object[name]; duplicate {
				return nil, errors.New("duplicate key")
			}
			object[name], err = parseValue(d)
			if err != nil {
				return nil, err
			}
		}
		_, err := d.Token()
		return object, err
	case json.Delim('['):
		array := []any{}
		for d.More() {
			value, err := parseValue(d)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		_, err := d.Token()
		return array, err
	default:
		return token, nil
	}
}

func defaultsTree(defaults any) (any, error) {
	raw, err := json.Marshal(defaults)
	if err != nil {
		return nil, errors.New("config: cannot encode defaults")
	}
	// Nil pointers and maps in constructor defaults are allowed. Source nulls
	// are rejected separately by parseJSON.
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var tree any
	if err := d.Decode(&tree); err != nil {
		return nil, errors.New("config: cannot decode defaults")
	}
	return tree, nil
}

// Standard typed decoding remains responsible for type, range and unknown-field
// checks. Error messages include setting names and expected types, never values.
func decodeTree(tree, dst any, path string) error {
	raw, err := json.Marshal(tree)
	if err != nil {
		return fmt.Errorf("config: cannot encode configuration at %s", path)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
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
		if path == "" {
			path = "configuration"
		}
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			return fmt.Errorf("config: %s: %s", path, err)
		}
		return fmt.Errorf("config: invalid JSON at %s", path)
	}
	return nil
}
