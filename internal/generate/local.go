package generate

import (
	"debug/buildinfo"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bingyuanng/koala/tools"
	"go.yaml.in/yaml/v3"
)

// localTemplate is an explicit alternative to BSR remote execution. It verifies
// module versions embedded by go install, preserves plugin options, and uses a
// single merged OpenAPI invocation. It never edits the application's settings.
func localTemplate(root, directory, stage string, pinned tools.Versions) (string, error) {
	type tool struct{ file, module string }
	pluginsByRemote := map[string]tool{
		"buf.build/protocolbuffers/go":       {"protoc-gen-go", "google.golang.org/protobuf"},
		"buf.build/grpc/go":                  {"protoc-gen-go-grpc", "google.golang.org/grpc/cmd/protoc-gen-go-grpc"},
		"buf.build/grpc-ecosystem/gateway":   {"protoc-gen-grpc-gateway", "github.com/grpc-ecosystem/grpc-gateway/v2"},
		"buf.build/grpc-ecosystem/openapiv2": {"protoc-gen-openapiv2", "github.com/grpc-ecosystem/grpc-gateway/v2"},
	}
	bytes, e := os.ReadFile(filepath.Join(root, "buf.gen.yaml"))
	if e != nil {
		return "", e
	}
	var settings map[string]any
	if e = yaml.Unmarshal(bytes, &settings); e != nil {
		return "", e
	}
	plugins, ok := settings["plugins"].([]any)
	if !ok {
		return "", fmt.Errorf("buf.gen.yaml requires plugins")
	}
	for _, plugin := range plugins {
		p, ok := plugin.(map[string]any)
		if !ok {
			return "", fmt.Errorf("invalid plugin declaration")
		}
		remote, _ := p["remote"].(string)
		name, version, _ := strings.Cut(remote, ":")
		tool, ok := pluginsByRemote[name]
		if !ok || version == "" || version != pinned.Plugins[name] {
			return "", fmt.Errorf("local mode requires the pinned Koala remote plugin declarations")
		}
		binary, e := filepath.Abs(filepath.Join(directory, tool.file))
		if e != nil {
			return "", e
		}
		info, e := buildinfo.ReadFile(binary)
		if e != nil || info.Main.Path != tool.module || info.Main.Version != version || info.Main.Replace != nil {
			return "", fmt.Errorf("%s must be installed from %s@%s", tool.file, tool.module, version)
		}
		delete(p, "remote")
		p["local"] = binary
		p["strategy"] = "all"
	}
	bytes, e = yaml.Marshal(settings)
	if e != nil {
		return "", e
	}
	path := filepath.Join(stage, "buf.local.yaml")
	return path, os.WriteFile(path, bytes, 0600)
}
