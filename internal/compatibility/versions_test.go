package compatibility

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bingyuanng/koala/tools"
	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/modfile"
)

func readVersionContract(t *testing.T) (string, tools.Versions) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	contract, err := tools.ReadVersions()
	if err != nil {
		t.Fatal(err)
	}
	return root, contract
}

func readVersionFile(t *testing.T, root, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestVersionContract(t *testing.T) {
	root, contract := readVersionContract(t)
	module, err := modfile.Parse("go.mod", readVersionFile(t, root, "go.mod"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if module.Go == nil || module.Go.Version != contract.Go {
		t.Errorf("go.mod minimum Go version must match %s", contract.Go)
	}

	for _, name := range []string{"mise.toml", "internal/scaffold/templates/project/mise.toml"} {
		contents := string(readVersionFile(t, root, name))
		check := func(key, version string) {
			t.Helper()
			// Check the pinned string declarations without adding a TOML runtime dependency.
			pattern := `(?m)^` + regexp.QuoteMeta(key) + `\s*=\s*` + regexp.QuoteMeta(fmt.Sprintf("%q", version)) + `\s*$`
			if !regexp.MustCompile(pattern).MatchString(contents) {
				t.Errorf("%s: %s must be pinned to %s", name, key, version)
			}
		}
		check("go", contract.Go)
		check("buf", contract.Buf)
		if name == "mise.toml" {
			for pkg, remote := range map[string]string{
				"google.golang.org/protobuf/cmd/protoc-gen-go":                      "buf.build/protocolbuffers/go",
				"google.golang.org/grpc/cmd/protoc-gen-go-grpc":                     "buf.build/grpc/go",
				"github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway": "buf.build/grpc-ecosystem/gateway",
				"github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2":    "buf.build/grpc-ecosystem/openapiv2",
			} {
				check(fmt.Sprintf("%q", "go:"+pkg), strings.TrimPrefix(contract.Plugins[remote], "v"))
			}
		}
	}

	cmd := exec.Command("go", "list", "-m", "-json", "all")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolve module versions: %v", err)
	}
	actual := map[string]string{}
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var item struct{ Path, Version string }
		if err := decoder.Decode(&item); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		actual[item.Path] = item.Version
	}
	for name, version := range contract.Modules {
		if actual[name] != version {
			t.Errorf("%s: expected %s, got %s", name, version, actual[name])
		}
	}

	expected := []string{}
	for name, version := range contract.Plugins {
		expected = append(expected, name+":"+version)
	}
	slices.Sort(expected)
	for _, name := range []string{"tools/buf.gen.yaml", "internal/testdata/runtime/buf.gen.yaml", "internal/scaffold/templates/project/buf.gen.yaml"} {
		var settings struct {
			Plugins []struct{ Remote string } `yaml:"plugins"`
		}
		if err := yaml.Unmarshal(readVersionFile(t, root, name), &settings); err != nil {
			t.Fatal(err)
		}
		actual := []string{}
		for _, plugin := range settings.Plugins {
			actual = append(actual, plugin.Remote)
		}
		slices.Sort(actual)
		if !reflect.DeepEqual(actual, expected) {
			t.Errorf("%s: remote plugins differ from tools/versions.json", name)
		}
	}
	for _, name := range []string{"buf.yaml", "buf.lock", "buf.gen.yaml"} {
		if !bytes.Equal(readVersionFile(t, root, "internal/testdata/runtime/"+name), readVersionFile(t, root, "internal/scaffold/templates/project/"+name)) {
			t.Errorf("%s: project template differs from the verified fixture", name)
		}
	}
	for _, name := range []string{"gateway", "openapiv2"} {
		if contract.Plugins["buf.build/grpc-ecosystem/"+name] != contract.Modules["github.com/grpc-ecosystem/grpc-gateway/v2"] {
			t.Errorf("%s generator/runtime mismatch", name)
		}
	}
}

func TestInstalledBufVersion(t *testing.T) {
	if os.Getenv("KOALA_CHECK_TOOLS") != "1" {
		t.Skip("set KOALA_CHECK_TOOLS=1 to check development tool installation")
	}
	_, contract := readVersionContract(t)
	output, err := exec.Command("buf", "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("Buf is unavailable; run mise trust and mise install: %v\n%s", err, output)
	}
	if version := strings.TrimSpace(string(output)); version != contract.Buf {
		t.Fatalf("Buf: expected %s, got %s", contract.Buf, version)
	}
}
