package generate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bingyuanng/koala/tools"
)

type Options struct {
	LocalPlugins string
	Buf          string
	Strict       bool
	Output       io.Writer
}

// Run only writes a registry after descriptors, factory signatures and concrete
// interface compatibility pass. Buf output is staged separately. Existing
// application shells are never overwritten.
func Run(ctx context.Context, root string, o Options) (result error) {
	if o.Output == nil {
		o.Output = io.Discard
	}
	cfg, e := readConfig(root)
	if e != nil {
		return e
	}
	if e = rejectSymlinks(root, []string{"proto", "gen", "internal/service", "internal/bootstrap", ".koala"}); e != nil {
		return e
	}
	outputsTxn := newOutputTransaction(root)
	committed := false
	defer func() {
		if !committed {
			result = errors.Join(result, outputsTxn.rollback())
		}
	}()
	sources, e := protoSources(root, cfg)
	if e != nil {
		return e
	}
	var buf string
	pinned, e := tools.ReadVersions()
	if e != nil {
		return fmt.Errorf("invalid embedded tool versions: %w", e)
	}
	if len(sources) > 0 {
		buf, e = bufBinary(o.Buf, pinned.Buf)
		if e != nil {
			return e
		}
		version, err := exec.CommandContext(ctx, buf, "--version").Output()
		if err != nil || strings.TrimSpace(string(version)) != pinned.Buf {
			return fmt.Errorf("Buf version mismatch: expected %s", pinned.Buf)
		}
	}
	stage, e := os.MkdirTemp("", "koala-gen-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)

	previous, manifestError := readManifest(root)
	if manifestError != nil && !os.IsNotExist(manifestError) {
		return manifestError
	}
	contracts, e := contractHash(root, cfg)
	if e != nil {
		return e
	}
	descriptors, outputs, cached := cachedTransports(root, previous, contracts)
	if cached {
		fmt.Fprintln(o.Output, "Reusing verified transport artifacts for unchanged contracts")
	} else if len(sources) == 0 {
		outputs = map[string][]byte{}
		if e = outputsTxn.install(outputs, previous); e != nil {
			return e
		}
		fmt.Fprintln(o.Output, "No protobuf contracts; generating an empty registry")
	} else {
		if e = command(ctx, root, o.Output, buf, "lint"); e != nil {
			return e
		}
		if e = command(ctx, root, o.Output, buf, "build", "-o", filepath.Join(stage, "descriptors.bin"), "--as-file-descriptor-set"); e != nil {
			return e
		}
		descriptors, e = os.ReadFile(filepath.Join(stage, "descriptors.bin"))
		if e != nil {
			return e
		}
		args := []string{"generate", "--output", stage}
		if o.LocalPlugins != "" {
			template, e := localTemplate(root, o.LocalPlugins, stage, pinned)
			if e != nil {
				return e
			}
			args = append(args, "--template", template)
		}
		if e = command(ctx, root, o.Output, buf, args...); e != nil {
			return e
		}
		outputs, e = generatedFiles(filepath.Join(stage, "gen"))
		if e != nil {
			return e
		}
		if spec, ok := outputs["gen/openapi/api.swagger.json"]; ok {
			if e = validateSpec(spec); e != nil {
				return e
			}
		}
		if e = outputsTxn.install(outputs, previous); e != nil {
			return fmt.Errorf("transport output update failed: %w", e)
		}
	}
	services, e := servicesFrom(root, cfg, descriptors)
	if e != nil {
		return e
	}
	if len(sources) > 0 {
		fmt.Fprintln(o.Output, "Buf transport outputs updated; registry checks follow")
		if e = command(ctx, root, o.Output, "go", "build", "-mod=mod", "./gen/go/..."); e != nil {
			return e
		}
	}
	factories, e := discover(ctx, root, cfg, services)
	if e != nil {
		return e
	}
	scaffolded := false
	for _, s := range services {
		if _, ok := factories[s.Name]; ok {
			continue
		}
		if o.Strict {
			return fmt.Errorf("%s: no factory; strict generation requires implemented services", s.Name)
		}
		if e = scaffold(root, cfg, s); e != nil {
			return e
		}
		scaffolded = true
	}
	if scaffolded {
		factories, e = discover(ctx, root, cfg, services)
		if e != nil {
			return e
		}
	}
	for i := range services {
		s := &services[i]
		f, ok := factories[s.Name]
		if !ok {
			return fmt.Errorf("%s: factory was not discovered", s.Name)
		}
		s.Factory = f
		for _, m := range s.Methods {
			if f.Methods[m] {
				continue
			}
			if o.Strict {
				return fmt.Errorf("%s/%s: inherited Unimplemented method", s.Name, m)
			}
			fmt.Fprintf(o.Output, "warning: %s/%s inherits Unimplemented\n", s.Name, m)
		}
	}
	openAPIImport := ""
	if _, ok := outputs["gen/openapi/api.swagger.json"]; ok {
		module, err := ApplicationImportPath(root)
		if err != nil {
			return err
		}
		openAPIImport = module + "/gen/openapi"
		embedSource := []byte(header + "package openapi\n\nimport _ \"embed\"\n\n//go:embed api.swagger.json\nvar Document []byte\n")
		outputs["gen/openapi/openapi_gen.go"] = embedSource
		if e = outputsTxn.writeOwned("gen/openapi/openapi_gen.go", embedSource, previous); e != nil {
			return e
		}
	}
	registry, e := registrySource(cfg, services, openAPIImport)
	if e != nil {
		return e
	}
	outputs[cfg.Registry.Output] = registry
	outputs[".koala/descriptors.bin"] = descriptors
	if e = outputsTxn.writeOwned(".koala/descriptors.bin", descriptors, previous); e != nil {
		return e
	}
	if e = outputsTxn.writeOwned(cfg.Registry.Output, registry, previous); e != nil {
		return e
	}
	if e = command(ctx, root, o.Output, "go", "build", "-mod=mod", "./internal/bootstrap"); e != nil {
		return fmt.Errorf("generated registry failed its consumer build: %w", e)
	}
	input, e := inputHash(ctx, root, cfg, services)
	if e != nil {
		return e
	}
	m := manifest{Version: 1, Contracts: contracts, Input: input, Outputs: map[string]string{}}
	for path, b := range outputs {
		m.Outputs[path] = hash(b)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if e = outputsTxn.write(manifestPath, append(b, '\n')); e != nil {
		return e
	}
	committed = true
	fmt.Fprintln(o.Output, "Generated service registry and freshness manifest")
	return nil
}

func command(ctx context.Context, root string, out io.Writer, name string, args ...string) error {
	for attempt := 0; ; attempt++ {
		var diagnostic bytes.Buffer
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = root
		cmd.Stdout = io.MultiWriter(out, &diagnostic)
		cmd.Stderr = cmd.Stdout
		e := cmd.Run()
		if e == nil {
			return nil
		}
		if attempt >= 3 || !strings.Contains(diagnostic.String(), "resource_exhausted") {
			return fmt.Errorf("%s failed: %w", name, e)
		}
		delay := time.Duration(10*(1<<attempt)) * time.Second
		fmt.Fprintf(out, "Buf rate limit; retrying in %s\n", delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

func bufBinary(explicit, version string) (string, error) {
	if explicit != "" {
		p, e := filepath.Abs(explicit)
		return p, e
	}
	p, e := exec.LookPath("buf")
	if e != nil {
		return "", fmt.Errorf("Buf %s is required; install it or pass --buf /path/to/buf", version)
	}
	return p, nil
}
