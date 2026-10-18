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
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n"), 0600)
	os.MkdirAll(filepath.Join(root, "proto/user/v1"), 0700)
	os.WriteFile(filepath.Join(root, "proto/user/v1/user.proto"), nil, 0600)
	app := &descriptorpb.FileDescriptorProto{Name: proto.String("user/v1/user.proto"), Package: proto.String("user.v1"), Options: &descriptorpb.FileOptions{GoPackage: proto.String("example.com/app/gen/go/user/v1;userv1")}, Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("UserService"), Method: []*descriptorpb.MethodDescriptorProto{{Name: proto.String("Get")}}}}}
	dependency := &descriptorpb.FileDescriptorProto{Name: proto.String("dependency.proto"), Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("MustNotHost")}}}
	bytes, _ := proto.Marshal(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{app, dependency}})
	services, e := servicesFrom(root, Config{ProtoRoot: "proto"}, bytes)
	if e != nil || len(services) != 1 {
		t.Fatalf("scope %v %v", services, e)
	}
	app.Service[0].Method[0].ServerStreaming = proto.Bool(true)
	bytes, _ = proto.Marshal(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{app}})
	if _, e = servicesFrom(root, Config{ProtoRoot: "proto"}, bytes); e == nil {
		t.Fatal("streaming accepted")
	}
}
func TestOwnershipAndSymlinks(t *testing.T) {
	root := t.TempDir()
	p := "gen/go/hand.go"
	os.MkdirAll(filepath.Join(root, "gen/go"), 0700)
	os.WriteFile(filepath.Join(root, p), []byte("package hand\n"), 0600)
	if e := writeOwned(root, p, []byte(header+"package generated\n"), manifest{}); e == nil {
		t.Fatal("handwritten source overwritten")
	}
	previous := manifest{Outputs: map[string]string{p: hash([]byte("old content"))}}
	if e := installOutputs(root, map[string][]byte{}, previous); e == nil {
		t.Fatal("modified owned source deleted")
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
	c.Registry.Package = "bootstrap"
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
