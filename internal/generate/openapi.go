package generate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// validateSpec checks the generator's merged document before publishing it.
// It does not claim that Protovalidate constraints are translated to OpenAPI.
func validateSpec(b []byte) error {
	var document map[string]any
	if e := json.Unmarshal(b, &document); e != nil {
		return fmt.Errorf("invalid generated OpenAPI JSON")
	}
	if document["swagger"] != "2.0" {
		return fmt.Errorf("expected generated OpenAPI 2.0")
	}
	operations := map[string]bool{}
	paths, _ := document["paths"].(map[string]any)
	for _, raw := range paths {
		methods, _ := raw.(map[string]any)
		for method, raw := range methods {
			switch method {
			case "get", "post", "put", "patch", "delete", "options", "head":
			default:
				continue
			}
			operation, _ := raw.(map[string]any)
			id, _ := operation["operationId"].(string)
			if id == "" || operations[id] {
				return fmt.Errorf("OpenAPI operation IDs must be nonempty and unique; set explicit operation_id annotations for ambiguous services")
			}
			operations[id] = true
		}
	}
	definitions, _ := document["definitions"].(map[string]any)
	var walk func(any) error
	walk = func(value any) error {
		switch v := value.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok && strings.HasPrefix(ref, "#/definitions/") {
				if _, ok = definitions[strings.TrimPrefix(ref, "#/definitions/")]; !ok {
					return fmt.Errorf("OpenAPI references a missing definition")
				}
			}
			for _, child := range v {
				if e := walk(child); e != nil {
					return e
				}
			}
		case []any:
			for _, child := range v {
				if e := walk(child); e != nil {
					return e
				}
			}
		}
		return nil
	}
	return walk(document)
}
