package generate

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Arbitrary descriptor bytes must either fail or produce stable service discovery
// after protobuf normalization. Imported files must not become hosted services.
func FuzzDescriptorDiscovery(f *testing.F) {
	root := f.TempDir()
	for path, data := range map[string][]byte{
		"go.mod":                   []byte("module example.com/app\n"),
		"proto/user/v1/user.proto": nil,
	} {
		dest := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			f.Fatal(err)
		}
		if err := os.WriteFile(dest, data, 0600); err != nil {
			f.Fatal(err)
		}
	}
	for _, name := range []string{"user/v1/user.proto", "imported.proto"} {
		seed, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
			Name: proto.String(name), Package: proto.String("user.v1"),
			Options: &descriptorpb.FileOptions{GoPackage: proto.String("example.com/app/gen/go/user/v1")},
			Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("UserService")}},
		}}})
		if err != nil {
			f.Fatal(err)
		}
		f.Add(seed)
	}
	f.Add([]byte{})
	f.Add([]byte{0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		services, err := servicesFrom(root, Config{}, data)
		if err != nil {
			return
		}
		var descriptors descriptorpb.FileDescriptorSet
		if err := proto.Unmarshal(data, &descriptors); err != nil {
			t.Fatalf("discovery accepted invalid protobuf: %v", err)
		}
		canonical, err := proto.Marshal(&descriptors)
		if err != nil {
			t.Fatal(err)
		}
		repeated, err := servicesFrom(root, Config{}, canonical)
		if err != nil || !reflect.DeepEqual(services, repeated) {
			t.Fatalf("protobuf normalization changed discovery: %v", err)
		}
		// A descriptor whose source is absent is an import, regardless of its services.
		for _, file := range descriptors.File {
			file.Name = proto.String("absent.proto")
		}
		imports, err := proto.Marshal(&descriptors)
		if err != nil {
			t.Fatal(err)
		}
		services, err = servicesFrom(root, Config{}, imports)
		if err != nil || len(services) != 0 {
			t.Fatalf("imported descriptors were hosted: %v, %v", services, err)
		}
	})
}
