// Package configjson parses configuration JSON without duplicate keys or nulls.
package configjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Decode preserves number text and requires exactly one JSON value.
func Decode(raw []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	value, err := parseValue(d)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON")
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
