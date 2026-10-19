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

func servicesFrom(root string, cfg Config, b []byte) ([]service, error) {
	var set descriptorpb.FileDescriptorSet
	if e := proto.Unmarshal(b, &set); e != nil {
		return nil, e
	}
	excluded := map[string]bool{}
	for _, n := range cfg.Exclude {
		excluded[n] = true
	}
	module, e := ApplicationImportPath(root)
	if e != nil {
		return nil, e
	}
	var result []service
	for _, file := range set.File {
		if _, e := os.Stat(filepath.Join(root, cfg.ProtoRoot, filepath.FromSlash(file.GetName()))); e != nil {
			continue
		}
		for _, desc := range file.Service {
			name := file.GetPackage() + "." + desc.GetName()
			if excluded[name] {
				delete(excluded, name)
				continue
			}
			pkg := strings.Split(file.GetOptions().GetGoPackage(), ";")[0]
			expected := module + "/gen/go/" + filepath.ToSlash(filepath.Dir(file.GetName()))
			if pkg != expected {
				return nil, fmt.Errorf("%s: go_package must be %s", name, expected)
			}
			s := service{Name: name, GoName: desc.GetName(), Import: pkg, Directory: strings.ToLower(strings.TrimSuffix(desc.GetName(), "Service")), FileSymbol: "File_" + fileSymbolPattern.ReplaceAllString(file.GetName(), "_")}
			if s.Directory == "" {
				return nil, fmt.Errorf("%s: empty service directory", name)
			}
			for _, m := range desc.Method {
				if m.GetClientStreaming() || m.GetServerStreaming() {
					return nil, fmt.Errorf("%s: streaming RPCs are unsupported", name)
				}
				s.Methods = append(s.Methods, m.GetName())
			}
			result = append(result, s)
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
		b, e := os.ReadFile(filepath.Join(current, "go.mod"))
		if e == nil {
			m, err := modfile.Parse(filepath.Join(current, "go.mod"), b, nil)
			if err != nil {
				return "", err
			}
			if m.Module == nil {
				return "", errors.New("go.mod has no module directive")
			}
			rel, e := filepath.Rel(current, root)
			if e != nil {
				return "", e
			}
			name := m.Module.Mod.Path
			if rel != "." {
				name += "/" + filepath.ToSlash(rel)
			}
			return name, nil
		}
		if !os.IsNotExist(e) {
			return "", e
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("go.mod not found")
		}
		current = parent
	}
}

func discover(ctx context.Context, root string, c Config, services []service) (map[string]factory, error) {
	if _, err := os.Stat(filepath.Join(root, "internal/service")); os.IsNotExist(err) && len(services) == 0 {
		return map[string]factory{}, nil
	}
	patterns := []string{c.ServicePackages}
	for _, s := range services {
		patterns = append(patterns, s.Import)
	}
	pkgs, e := packages.Load(&packages.Config{Context: ctx, Dir: root, BuildFlags: []string{"-mod=mod"}, Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo}, patterns...)
	if e != nil {
		return nil, e
	}
	known := map[string]service{}
	interfaces := map[string]types.Type{}
	packageTypes := map[string]*types.Package{}
	for _, p := range pkgs {
		packageTypes[p.PkgPath] = p.Types
	}
	for _, s := range services {
		known[s.Name] = s
		if p := packageTypes[s.Import]; p != nil {
			if obj := p.Scope().Lookup(s.GoName + "Server"); obj != nil {
				interfaces[s.Name] = obj.Type()
			}
		}
	}
	found := map[string]factory{}
	for _, p := range pkgs {
		if !strings.Contains(p.PkgPath, "/internal/service/") {
			continue
		}
		if len(p.Errors) > 0 {
			return nil, fmt.Errorf("factory package %s: %s", p.PkgPath, p.Errors[0].Msg)
		}
		for _, file := range p.Syntax {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Doc == nil {
					continue
				}
				mapping := ""
				for _, comment := range fn.Doc.List {
					if strings.HasPrefix(comment.Text, "//koala:service ") {
						if mapping != "" {
							return nil, fmt.Errorf("%s: multiple factory annotations", fn.Name.Name)
						}
						mapping = strings.TrimSpace(strings.TrimPrefix(comment.Text, "//koala:service "))
					}
				}
				if mapping == "" {
					continue
				}
				if slices.Contains(c.Exclude, mapping) {
					continue
				}
				if _, ok := known[mapping]; !ok {
					return nil, fmt.Errorf("%s.%s: unknown service annotation %s", p.PkgPath, fn.Name.Name, mapping)
				}
				if _, ok := found[mapping]; ok {
					return nil, fmt.Errorf("%s: duplicate factories", mapping)
				}
				factory, err := inspectFactory(p, fn, known[mapping], interfaces[mapping], c.DependencyType)
				if err != nil {
					return nil, err
				}
				found[mapping] = factory
			}
		}
	}
	return found, nil
}

// inspectFactory checks the annotated declaration against the generated server.
func inspectFactory(p *packages.Package, fn *ast.FuncDecl, service service, serverInterface types.Type, dependencyType string) (factory, error) {
	mapping := service.Name
	depImport, depName, _ := splitType(dependencyType)
	object, ok := p.TypesInfo.Defs[fn.Name].(*types.Func)
	if !ok {
		return factory{}, fmt.Errorf("%s: unresolved factory", mapping)
	}
	sig := object.Type().(*types.Signature)
	if fn.Recv != nil || !ast.IsExported(fn.Name.Name) || sig.TypeParams().Len() != 0 || sig.Params().Len() != 1 || sig.Results().Len() != 2 || sig.Variadic() {
		return factory{}, fmt.Errorf("%s: factory must be exported func(*deps.Dependencies) (*Concrete,error)", mapping)
	}
	pointer, ok := sig.Params().At(0).Type().(*types.Pointer)
	if !ok {
		return factory{}, fmt.Errorf("%s: factory dependency must be a pointer", mapping)
	}
	named, ok := pointer.Elem().(*types.Named)
	if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != depImport || named.Obj().Name() != depName {
		return factory{}, fmt.Errorf("%s: wrong factory dependency type", mapping)
	}
	result, ok := sig.Results().At(0).Type().(*types.Pointer)
	if !ok {
		return factory{}, fmt.Errorf("%s: factory must return a concrete pointer", mapping)
	}
	concrete, ok := result.Elem().(*types.Named)
	if !ok || concrete.Obj().Pkg() == nil || concrete.Obj().Pkg().Path() != p.PkgPath || !ast.IsExported(concrete.Obj().Name()) {
		return factory{}, fmt.Errorf("%s: factory result must be an exported local type", mapping)
	}
	if !types.Identical(sig.Results().At(1).Type(), types.Universe.Lookup("error").Type()) {
		return factory{}, fmt.Errorf("%s: factory second result must be error", mapping)
	}
	iface := serverInterface
	if iface == nil || !types.AssignableTo(result, iface) {
		return factory{}, fmt.Errorf("%s: concrete type does not implement generated server", mapping)
	}
	methods := map[string]bool{}
	methodSet := types.NewMethodSet(result)
	for i := 0; i < methodSet.Len(); i++ {
		method := methodSet.At(i).Obj().(*types.Func)
		receiver := method.Type().(*types.Signature).Recv().Type()
		if ptr, ok := receiver.(*types.Pointer); ok {
			receiver = ptr.Elem()
		}
		inheritedFallback := false
		if named, ok := receiver.(*types.Named); ok {
			inheritedFallback = named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == service.Import && named.Obj().Name() == "Unimplemented"+service.GoName+"Server"
		}
		if !inheritedFallback {
			methods[method.Name()] = true
		}
	}
	return factory{Import: p.PkgPath, Name: fn.Name.Name, Signature: types.TypeString(sig, func(p *types.Package) string { return p.Path() }), Methods: methods}, nil
}

// ReportFallbacks rechecks concrete declarations at startup. Business methods
// are inspected for diagnostics, while remaining excluded from freshness hashes.
func ReportFallbacks(ctx context.Context, root string, out io.Writer) error {
	c, e := readConfig(root)
	if e != nil {
		return e
	}
	_, e = readManifest(root)
	if e != nil {
		return e
	}
	services, e := servicesFromManifest(root)
	if e != nil {
		return e
	}
	factories, e := discover(ctx, root, c, services)
	if e != nil {
		return e
	}
	for _, s := range services {
		for _, method := range s.Methods {
			if !factories[s.Name].Methods[method] {
				fmt.Fprintf(out, "warning: %s/%s inherits Unimplemented\n", s.Name, method)
			}
		}
	}
	return nil
}
