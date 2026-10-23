package generate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestDescriptorScopeAndStreaming(t *testing.T) {
	for _, tt := range []struct {
		name, source, wantError    string
		clientStream, serverStream bool
		wantServices               int
	}{
		{name: "application and imported dependency", source: "file", wantServices: 1},
		{name: "imported descriptors only", source: "missing"},
		{name: "filesystem failure", source: "symlink loop", wantError: "inspect protobuf source user/v1/user.proto"},
		{name: "client streaming", source: "file", clientStream: true, wantError: "streaming RPCs are unsupported"},
		{name: "server streaming", source: "file", serverStream: true, wantError: "streaming RPCs are unsupported"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "proto/user/v1/user.proto")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			switch tt.source {
			case "file":
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink loop":
				if err := os.Symlink(path, path); err != nil {
					t.Fatal(err)
				}
			}
			app := &descriptorpb.FileDescriptorProto{
				Name: proto.String("user/v1/user.proto"), Package: proto.String("user.v1"),
				Options: &descriptorpb.FileOptions{GoPackage: proto.String("example.com/app/gen/go/user/v1;userv1")},
				Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("UserService"), Method: []*descriptorpb.MethodDescriptorProto{{
					Name: proto.String("Get"), ClientStreaming: proto.Bool(tt.clientStream), ServerStreaming: proto.Bool(tt.serverStream),
				}}}},
			}
			dependency := &descriptorpb.FileDescriptorProto{Name: proto.String("dependency.proto"), Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("MustNotHost")}}}
			data, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{app, dependency}})
			if err != nil {
				t.Fatal(err)
			}
			services, err := servicesFrom(root, Config{}, data)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("expected %q, got %v", tt.wantError, err)
				}
				return
			}
			if err != nil || len(services) != tt.wantServices {
				t.Fatalf("services = %v, error = %v", services, err)
			}
			if len(services) > 0 && services[0].Name != "user.v1.UserService" {
				t.Fatalf("hosted an imported service: %v", services)
			}
		})
	}
}

func TestOwnershipAndSymlinks(t *testing.T) {
	root := t.TempDir()
	p := "gen/go/hand.go"
	os.MkdirAll(filepath.Join(root, "gen/go"), 0700)
	os.WriteFile(filepath.Join(root, p), []byte("package hand\n"), 0600)
	if e := newOutputTransaction(root).writeOutputs(map[string][]byte{p: []byte(header + "package generated\n")}, manifest{}); e == nil {
		t.Fatal("handwritten source overwritten")
	}
	previous := manifest{Outputs: map[string]string{p: hash([]byte("old content"))}}
	if e := newOutputTransaction(root).install(map[string][]byte{}, previous); e != nil {
		t.Fatal("modified obsolete output was not removed", e)
	}
	if _, err := os.Stat(filepath.Join(root, p)); !os.IsNotExist(err) {
		t.Fatal("obsolete output still exists", err)
	}
	if e := os.Symlink(t.TempDir(), filepath.Join(root, "proto")); e != nil {
		t.Fatal(e)
	}
	if e := rejectSymlinks(root, []string{"proto"}); e == nil {
		t.Fatal("symlink accepted")
	}
}

func TestRegistryIsDeterministic(t *testing.T) {
	c := Config{DependencyType: "example.com/app/internal/deps.Dependencies"}
	services := []service{{Name: "user.v1.UserService", GoName: "UserService", Import: "example.com/app/gen/go/user/v1", FileSymbol: "File_user_v1_user_proto", Factory: factory{Import: "example.com/app/internal/service/user", Name: "New"}}}
	first, e := registrySource(c, services, "example.com/app/gen/openapi")
	if e != nil {
		t.Fatal(e)
	}
	second, _ := registrySource(c, services, "example.com/app/gen/openapi")
	if string(first) != string(second) || !strings.Contains(string(first), "RegisterUserServiceHandler") {
		t.Fatal("registry contract")
	}
}

func TestMergedOpenAPIValidation(t *testing.T) {
	good := []byte(`{"swagger":"2.0","paths":{"/first":{"get":{"operationId":"First_Get","responses":{"200":{"schema":{"$ref":"#/definitions/Response"}}}}},"/second":{"get":{"operationId":"Second_Get"}}},"definitions":{"Response":{"type":"object"}}}`)
	if e := validateSpec(good); e != nil {
		t.Fatal(e)
	}
	duplicate := strings.Replace(string(good), "Second_Get", "First_Get", 1)
	if validateSpec([]byte(duplicate)) == nil {
		t.Fatal("duplicate operation IDs accepted")
	}
	missing := strings.Replace(string(good), "#/definitions/Response", "#/definitions/Missing", 1)
	if validateSpec([]byte(missing)) == nil {
		t.Fatal("missing schema accepted")
	}
}

func TestFreshnessRequiresProjectConfig(t *testing.T) {
	if err := CheckFresh(context.Background(), t.TempDir()); err == nil {
		t.Fatal("missing config accepted")
	}
}
