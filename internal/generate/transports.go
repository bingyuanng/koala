package generate

import (
	"context"
	"fmt"
	"github.com/bingyuanng/koala/tools"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type transportArtifacts struct {
	descriptors []byte
	outputs     map[string][]byte
	cached      bool
	hasSources  bool
}

func prepareTransports(ctx context.Context, root string, previous manifest, contracts string, o Options) (*transportArtifacts, error) {
	sources, err := protoSources(root)
	if err != nil {
		return nil, err
	}
	pinned, err := tools.ReadVersions()
	if err != nil {
		return nil, fmt.Errorf("invalid embedded tool versions: %w", err)
	}
	var buf string
	if len(sources) > 0 {
		buf, err = bufBinary(o.Buf, pinned.Buf)
		if err != nil {
			return nil, err
		}
		version, err := exec.CommandContext(ctx, buf, "--version").Output()
		if err != nil || strings.TrimSpace(string(version)) != pinned.Buf {
			return nil, fmt.Errorf("Buf version mismatch: expected %s", pinned.Buf)
		}
	}
	descriptors, outputs, cached := cachedTransports(root, previous, contracts)
	artifacts := &transportArtifacts{descriptors: descriptors, outputs: outputs, cached: cached, hasSources: len(sources) > 0}
	if cached {
		fmt.Fprintln(o.Output, "Reusing verified transport artifacts for unchanged contracts")
		return artifacts, nil
	}
	if !artifacts.hasSources {
		artifacts.outputs = map[string][]byte{}
		fmt.Fprintln(o.Output, "No protobuf contracts; generating an empty registry")
		return artifacts, nil
	}
	stage, err := os.MkdirTemp("", "koala-gen-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	if err := command(ctx, root, o.Output, buf, "lint"); err != nil {
		return nil, err
	}
	if err := command(ctx, root, o.Output, buf, "build", "-o", filepath.Join(stage, "descriptors.bin"), "--as-file-descriptor-set"); err != nil {
		return nil, err
	}
	artifacts.descriptors, err = os.ReadFile(filepath.Join(stage, "descriptors.bin"))
	if err != nil {
		return nil, err
	}
	template, err := localTemplate(root, stage, pinned)
	if err != nil {
		return nil, err
	}
	args := []string{"generate", "--output", stage, "--template", template}
	if err := command(ctx, root, o.Output, buf, args...); err != nil {
		return nil, err
	}
	artifacts.outputs, err = generatedFiles(filepath.Join(stage, "gen"))
	if err != nil {
		return nil, err
	}
	if spec, ok := artifacts.outputs["gen/openapi/api.swagger.json"]; ok {
		if err := validateSpec(spec); err != nil {
			return nil, err
		}
	}
	return artifacts, nil
}
