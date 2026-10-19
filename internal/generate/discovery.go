package generate

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/packages"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

var fileSymbolPattern = regexp.MustCompile(`[^A-Za-z0-9]`)

type service struct {
	Name, GoName, Import, FileSymbol, Directory string
	Methods                                     []string
	Factory                                     factory
}
type factory struct {
	Import, Name, Signature string
	Methods                 map[string]bool
}

func servicesFrom(root string, cfg Config, descriptors []byte) ([]service, error) {
	var descriptorSet descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(descriptors, &descriptorSet); err != nil {
		return nil, err
	}
	excluded := map[string]bool{}
	for _, name := range cfg.Exclude {
		excluded[name] = true
	}
	module, err := ApplicationImportPath(root)
	if err != nil {
		return nil, err
	}
	var result []service
	for _, file := range descriptorSet.File {
		if _, err := os.Stat(filepath.Join(root, protoDirectory, filepath.FromSlash(file.GetName()))); err != nil {
			continue
		}
		for _, descriptor := range file.Service {
			name := file.GetPackage() + "." + descriptor.GetName()
			if excluded[name] {
				delete(excluded, name)
				continue
			}
			importPath := strings.Split(file.GetOptions().GetGoPackage(), ";")[0]
			expectedImport := module + "/gen/go/" + filepath.ToSlash(filepath.Dir(file.GetName()))
			if importPath != expectedImport {
				return nil, fmt.Errorf("%s: go_package must be %s", name, expectedImport)
			}
			definition := service{
				Name:       name,
				GoName:     descriptor.GetName(),
				Import:     importPath,
				Directory:  strings.ToLower(strings.TrimSuffix(descriptor.GetName(), "Service")),
				FileSymbol: "File_" + fileSymbolPattern.ReplaceAllString(file.GetName(), "_"),
			}
			if definition.Directory == "" {
				return nil, fmt.Errorf("%s: empty service directory", name)
			}
			for _, method := range descriptor.Method {
				if method.GetClientStreaming() || method.GetServerStreaming() {
					return nil, fmt.Errorf("%s: streaming RPCs are unsupported", name)
				}
				definition.Methods = append(definition.Methods, method.GetName())
			}
			result = append(result, definition)
		}
	}
	if len(excluded) > 0 {
		return nil, fmt.Errorf("exclude references unknown services")
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// ApplicationImportPath resolves a project inside a standalone module or monorepo.
func ApplicationImportPath(root string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	current := root
	for {
		contents, err := os.ReadFile(filepath.Join(current, "go.mod"))
		if err == nil {
			moduleFile, err := modfile.Parse(filepath.Join(current, "go.mod"), contents, nil)
			if err != nil {
				return "", err
			}
			if moduleFile.Module == nil {
				return "", errors.New("go.mod has no module directive")
			}
			relativePath, err := filepath.Rel(current, root)
			if err != nil {
				return "", err
			}
			importPath := moduleFile.Module.Mod.Path
			if relativePath != "." {
				importPath += "/" + filepath.ToSlash(relativePath)
			}
			return importPath, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("go.mod not found")
		}
		current = parent
	}
}

func discover(ctx context.Context, root string, cfg Config, services []service) (map[string]factory, error) {
	if _, err := os.Stat(filepath.Join(root, "internal/service")); os.IsNotExist(err) && len(services) == 0 {
		return map[string]factory{}, nil
	}
	patterns := []string{servicePackages}
	for _, definition := range services {
		patterns = append(patterns, definition.Import)
	}
	loadedPackages, err := packages.Load(&packages.Config{
		Context:    ctx,
		Dir:        root,
		BuildFlags: []string{"-mod=mod"},
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
	}, patterns...)
	if err != nil {
		return nil, err
	}
	servicesByName := map[string]service{}
	serverInterfaces := map[string]types.Type{}
	packageTypes := map[string]*types.Package{}
	for _, pkg := range loadedPackages {
		packageTypes[pkg.PkgPath] = pkg.Types
	}
	for _, definition := range services {
		servicesByName[definition.Name] = definition
		if pkg := packageTypes[definition.Import]; pkg != nil {
			if serverType := pkg.Scope().Lookup(definition.GoName + "Server"); serverType != nil {
				serverInterfaces[definition.Name] = serverType.Type()
			}
		}
	}
	factories := map[string]factory{}
	for _, pkg := range loadedPackages {
		if !strings.Contains(pkg.PkgPath, "/internal/service/") {
			continue
		}
		if len(pkg.Errors) > 0 {
			return nil, fmt.Errorf("factory package %s: %s", pkg.PkgPath, pkg.Errors[0].Msg)
		}
		for _, file := range pkg.Syntax {
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Doc == nil {
					continue
				}
				mapping, err := serviceAnnotation(function)
				if err != nil {
					return nil, err
				}
				if mapping == "" {
					continue
				}
				if slices.Contains(cfg.Exclude, mapping) {
					continue
				}
				if _, ok := servicesByName[mapping]; !ok {
					return nil, fmt.Errorf("%s.%s: unknown service annotation %s", pkg.PkgPath, function.Name.Name, mapping)
				}
				if _, ok := factories[mapping]; ok {
					return nil, fmt.Errorf("%s: duplicate factories", mapping)
				}
				factory, err := inspectFactory(pkg, function, servicesByName[mapping], serverInterfaces[mapping], cfg.DependencyType)
				if err != nil {
					return nil, err
				}
				factories[mapping] = factory
			}
		}
	}
	return factories, nil
}

// serviceAnnotation reads the service mapping without loading or inspecting types.
func serviceAnnotation(function *ast.FuncDecl) (string, error) {
	mapping := ""
	for _, comment := range function.Doc.List {
		if !strings.HasPrefix(comment.Text, "//koala:service ") {
			continue
		}
		if mapping != "" {
			return "", fmt.Errorf("%s: multiple factory annotations", function.Name.Name)
		}
		mapping = strings.TrimSpace(strings.TrimPrefix(comment.Text, "//koala:service "))
	}
	return mapping, nil
}

// inspectFactory checks the annotated declaration against the generated server.
func inspectFactory(pkg *packages.Package, function *ast.FuncDecl, definition service, serverInterface types.Type, dependencyType string) (factory, error) {
	serviceName := definition.Name
	dependencyImport, dependencyName, _ := splitType(dependencyType)
	object, ok := pkg.TypesInfo.Defs[function.Name].(*types.Func)
	if !ok {
		return factory{}, fmt.Errorf("%s: unresolved factory", serviceName)
	}
	signature := object.Type().(*types.Signature)
	if function.Recv != nil || !ast.IsExported(function.Name.Name) ||
		signature.TypeParams().Len() != 0 || signature.Variadic() ||
		signature.Params().Len() != 1 || signature.Results().Len() != 2 {
		return factory{}, fmt.Errorf("%s: factory must be exported func(*deps.Dependencies) (*Concrete,error)", serviceName)
	}
	dependencyPointer, ok := signature.Params().At(0).Type().(*types.Pointer)
	if !ok {
		return factory{}, fmt.Errorf("%s: factory dependency must be a pointer", serviceName)
	}
	dependencyTypeName, ok := dependencyPointer.Elem().(*types.Named)
	if !ok || dependencyTypeName.Obj().Pkg() == nil ||
		dependencyTypeName.Obj().Pkg().Path() != dependencyImport || dependencyTypeName.Obj().Name() != dependencyName {
		return factory{}, fmt.Errorf("%s: wrong factory dependency type", serviceName)
	}
	implementation, ok := signature.Results().At(0).Type().(*types.Pointer)
	if !ok {
		return factory{}, fmt.Errorf("%s: factory must return a concrete pointer", serviceName)
	}
	implementationType, ok := implementation.Elem().(*types.Named)
	if !ok || implementationType.Obj().Pkg() == nil ||
		implementationType.Obj().Pkg().Path() != pkg.PkgPath || !ast.IsExported(implementationType.Obj().Name()) {
		return factory{}, fmt.Errorf("%s: factory result must be an exported local type", serviceName)
	}
	if !types.Identical(signature.Results().At(1).Type(), types.Universe.Lookup("error").Type()) {
		return factory{}, fmt.Errorf("%s: factory second result must be error", serviceName)
	}
	if serverInterface == nil || !types.AssignableTo(implementation, serverInterface) {
		return factory{}, fmt.Errorf("%s: concrete type does not implement generated server", serviceName)
	}

	return factory{
		Import:    pkg.PkgPath,
		Name:      function.Name.Name,
		Signature: types.TypeString(signature, func(pkg *types.Package) string { return pkg.Path() }),
		Methods:   implementedMethods(implementation, definition),
	}, nil
}

// implementedMethods excludes the generated fallback receiver while retaining
// application methods, including promoted methods from application types.
func implementedMethods(implementation types.Type, definition service) map[string]bool {
	methods := map[string]bool{}
	methodSet := types.NewMethodSet(implementation)
	for i := 0; i < methodSet.Len(); i++ {
		method := methodSet.At(i).Obj().(*types.Func)
		receiver := method.Type().(*types.Signature).Recv().Type()
		if receiverPointer, ok := receiver.(*types.Pointer); ok {
			receiver = receiverPointer.Elem()
		}
		inheritedFallback := false
		if receiverType, ok := receiver.(*types.Named); ok {
			inheritedFallback = receiverType.Obj().Pkg() != nil &&
				receiverType.Obj().Pkg().Path() == definition.Import &&
				receiverType.Obj().Name() == "Unimplemented"+definition.GoName+"Server"
		}
		if !inheritedFallback {
			methods[method.Name()] = true
		}
	}
	return methods
}

// ReportFallbacks rechecks concrete declarations at startup. Business methods
// are inspected for diagnostics, while remaining excluded from freshness hashes.
func ReportFallbacks(ctx context.Context, root string, out io.Writer) error {
	cfg, err := readConfig(root)
	if err != nil {
		return err
	}
	_, err = readManifest(root)
	if err != nil {
		return err
	}
	services, err := servicesFromManifest(root, cfg)
	if err != nil {
		return err
	}
	factories, err := discover(ctx, root, cfg, services)
	if err != nil {
		return err
	}
	for _, definition := range services {
		for _, method := range definition.Methods {
			if !factories[definition.Name].Methods[method] {
				fmt.Fprintf(out, "warning: %s/%s inherits Unimplemented\n", definition.Name, method)
			}
		}
	}
	return nil
}
