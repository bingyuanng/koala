package generate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
)

type Options struct {
	Buf    string
	Strict bool
	Output io.Writer
}

// Run stages transports, installs them for type checking, resolves services,
// renders registration, then validates and commits all generated outputs.
func Run(ctx context.Context, root string, o Options) (result error) {
	if o.Output == nil {
		o.Output = io.Discard
	}
	cfg, err := readConfig(root)
	if err != nil {
		return err
	}
	if err := rejectSymlinks(root, []string{"proto", "gen", "internal/service", "internal/bootstrap", ".koala"}); err != nil {
		return err
	}
	previous, err := readManifest(root)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	contracts, err := contractHash(root)
	if err != nil {
		return err
	}
	transports, err := prepareTransports(ctx, root, previous, contracts, o)
	if err != nil {
		return err
	}

	// Project output mutations start here and remain reversible until commit.
	transaction := newOutputTransaction(root)
	defer func() { result = errors.Join(result, transaction.rollback()) }()
	if !transports.cached {
		if err := transaction.install(transports.outputs, previous); err != nil {
			return fmt.Errorf("transport output update failed: %w", err)
		}
	}
	if transports.hasSources {
		fmt.Fprintln(o.Output, "Buf transport outputs updated; registry checks follow")
		if err := command(ctx, root, o.Output, "go", "build", "-mod=mod", "./gen/go/..."); err != nil {
			return err
		}
	}
	services, err := resolveServices(ctx, root, cfg, transports.descriptors, o)
	if err != nil {
		return err
	}
	module, err := ApplicationImportPath(root)
	if err != nil {
		return err
	}
	_, hasOpenAPI := transports.outputs["gen/openapi/api.swagger.json"]
	registration, err := registrationOutputs(module, cfg, services, transports.descriptors, hasOpenAPI)
	if err != nil {
		return err
	}
	if err := transaction.writeOutputs(registration, previous); err != nil {
		return err
	}
	if err := command(ctx, root, o.Output, "go", "build", "-mod=mod", "./internal/bootstrap"); err != nil {
		return fmt.Errorf("generated registry failed its consumer build: %w", err)
	}
	input, err := inputHash(ctx, root, cfg, services)
	if err != nil {
		return err
	}
	maps.Copy(transports.outputs, registration)
	if err := transaction.commit(contracts, input, transports.outputs); err != nil {
		return err
	}
	fmt.Fprintln(o.Output, "Generated service registry and freshness manifest")
	return nil
}
