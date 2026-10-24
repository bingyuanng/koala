// Verify fixture generation without overwriting checked-in artifacts.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	s "github.com/bingyuanng/koala/scripts/internal/scriptutil"
	"github.com/bingyuanng/koala/tools"
)

func main() { s.Main(checkGeneration) }
func checkGeneration() {
	flag.Parse()
	s.Check(flag.NArg() == 0, "unexpected arguments: %v", flag.Args())
	root := s.Root()
	fixture := filepath.Join(root, "internal/testdata/runtime")
	s.Run(root, s.Env(map[string]string{"KOALA_CHECK_TOOLS": "1"}, ""), false, "go", "test", "-count=1", "./internal/compatibility", "-run", "TestVersionContract|TestInstalledBufVersion")
	s.Run(fixture, nil, false, "buf", "format", "--diff", "--exit-code")
	s.Run(fixture, nil, false, "buf", "lint")
	dir := s.Temp("koala-generation-")
	defer os.RemoveAll(dir)
	template := s.Read(filepath.Join(fixture, "buf.gen.yaml"))
	contract, err := tools.ReadVersions()
	s.Must(err)
	for _, plugin := range [][3]string{
		{"protocolbuffers/go", "protoc-gen-go", "google.golang.org/protobuf"},
		{"grpc/go", "protoc-gen-go-grpc", "google.golang.org/grpc/cmd/protoc-gen-go-grpc"},
		{"grpc-ecosystem/gateway", "protoc-gen-grpc-gateway", "github.com/grpc-ecosystem/grpc-gateway/v2"},
		{"grpc-ecosystem/openapiv2", "protoc-gen-openapiv2", "github.com/grpc-ecosystem/grpc-gateway/v2"},
	} {
		version := contract.Plugins["buf.build/"+plugin[0]]
		binary, err := tools.ResolveBinary(root, plugin[1])
		s.Must(err)
		metadata := s.Run(root, nil, false, "go", "version", "-m", binary)
		s.Check(strings.Contains(metadata, "\tmod\t"+plugin[2]+"\t"+version+"\t"), "%s does not match its pinned module version", plugin[1])
		template = strings.ReplaceAll(template, "local: "+plugin[1]+"\n", "local: "+binary+"\n")
	}
	localTemplate := filepath.Join(dir, "buf.local.yaml")
	s.Write(localTemplate, template)
	templateArgs := []string{"--template", localTemplate}
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	for _, output := range []string{first, second} {
		s.Run(fixture, nil, false, append([]string{"buf", "generate", "--output", output}, templateArgs...)...)
	}
	generated := s.Snapshot(filepath.Join(first, "gen"))
	s.Check(len(generated) > 0, "Generation produced no artifacts")
	s.Compare(generated, s.Snapshot(filepath.Join(second, "gen")), "Repeat generation differs")
	s.Compare(generated, s.Snapshot(filepath.Join(fixture, "gen")), "Checked-in artifacts are stale")
	health := filepath.Join(root, "internal/scaffold/testdata/health")
	healthTemplate := templateArgs
	s.Run(health, nil, false, "buf", "format", "--diff", "--exit-code")
	s.Run(health, nil, false, "buf", "lint")
	outputs := []string{filepath.Join(dir, "health-first"), filepath.Join(dir, "health-second")}
	for _, output := range outputs {
		s.Run(health, nil, false, append([]string{"buf", "generate", "--output", output}, healthTemplate...)...)
	}
	s.Compare(s.Snapshot(filepath.Join(outputs[0], "gen/go")), s.Snapshot(filepath.Join(outputs[1], "gen/go")), "Repeat health generation differs")
	s.Compare(s.Snapshot(filepath.Join(outputs[0], "gen/go")), s.Snapshot(filepath.Join(health, "gen/go")), "Health transport fixture is stale")
	s.Run(root, nil, false, "go", "build", "-mod=readonly", "./internal/testdata/runtime/gen/go/...")
	fmt.Println("Buf lint/format and deterministic generation match checked-in artifacts; fixture builds")
}
