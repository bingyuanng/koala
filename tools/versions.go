// Package tools embeds the version contract used by the CLI and verification scripts.
package tools

import (
	_ "embed"
	"encoding/json"
)

//go:embed versions.json
var versionContract []byte

type Versions struct {
	Go      string            `json:"go"`
	Buf     string            `json:"buf"`
	Plugins map[string]string `json:"plugins"`
	Modules map[string]string `json:"modules"`
}

// ReadVersions returns an independent copy of the embedded tools/versions.json.
func ReadVersions() (Versions, error) {
	var versions Versions
	err := json.Unmarshal(versionContract, &versions)
	return versions, err
}
