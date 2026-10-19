package generate

import (
	"context"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
)

// resolveServices discovers and validates factories. Missing application-owned
// shells are created once, outside the generated-output transaction.
func resolveServices(ctx context.Context, root string, cfg Config, descriptors []byte, o Options) ([]service, error) {
	services, err := servicesFrom(root, cfg, descriptors)
	if err != nil {
		return nil, err
	}
	factories, err := discover(ctx, root, cfg, services)
	if err != nil {
		return nil, err
	}
	scaffolded := false
	for _, definition := range services {
		if _, ok := factories[definition.Name]; ok {
			continue
		}
		if o.Strict {
			return nil, fmt.Errorf("%s: no factory; strict generation requires implemented services", definition.Name)
		}
		if err = scaffold(root, cfg, definition); err != nil {
			return nil, err
		}
		scaffolded = true
	}
	if scaffolded {
		factories, err = discover(ctx, root, cfg, services)
		if err != nil {
			return nil, err
		}
	}
	for i := range services {
		definition := &services[i]
		factory, ok := factories[definition.Name]
		if !ok {
			return nil, fmt.Errorf("%s: factory was not discovered", definition.Name)
		}
		definition.Factory = factory
		for _, method := range definition.Methods {
			if factory.Methods[method] {
				continue
			}
			if o.Strict {
				return nil, fmt.Errorf("%s/%s: inherited Unimplemented method", definition.Name, method)
			}
			fmt.Fprintf(o.Output, "warning: %s/%s inherits Unimplemented\n", definition.Name, method)
		}
	}
	return services, nil
}

func scaffold(root string, cfg Config, definition service) error {
	dependencyImport, dependencyType, _ := splitType(cfg.DependencyType)
	directory := filepath.Join(root, "internal/service", definition.Directory)
	entries, err := os.ReadDir(directory)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s: no mapped factory and service directory already exists; annotate its factory", definition.Name)
	}
	source := fmt.Sprintf("// Application-owned service. Koala will never overwrite this file.\npackage service\nimport(pb %q;deps %q)\ntype Service struct{pb.Unimplemented%sServer}\n//koala:service %s\nfunc New(d *deps.%s)(*Service,error){return &Service{},nil}\n", definition.Import, dependencyImport, definition.GoName, definition.Name, dependencyType)
	formatted, err := format.Source([]byte(source))
	if err != nil {
		return err
	}
	if err = os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(directory, "service.go"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, err = file.Write(formatted)
	return errors.Join(err, file.Close())
}
