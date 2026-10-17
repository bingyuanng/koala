package users_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"buf.build/go/protovalidate"
	userv1 "github.com/bingyuanng/koala/examples/users/gen/go/user/v1"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The fixture must support connection-based gateway registration, which
// preserves the gRPC interceptor path when the runtime is added in Phase 3.
var _ func(context.Context, *runtime.ServeMux, *grpc.ClientConn) error = userv1.RegisterUserServiceHandler

func TestValidationAnnotations(t *testing.T) {
	validator, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		message proto.Message
		valid   bool
	}{
		{&userv1.CreateUserRequest{Name: "Ada", Email: "ada@example.com"}, true},
		{&userv1.CreateUserRequest{Name: "", Email: "ada@example.com"}, false},
		{&userv1.CreateUserRequest{Name: "Ada", Email: "invalid"}, false},
		{&userv1.GetUserRequest{Id: "1"}, true},
		{&userv1.GetUserRequest{}, false},
	}
	for _, tt := range tests {
		encoded, err := proto.Marshal(tt.message)
		if err != nil {
			t.Fatal(err)
		}
		decoded := tt.message.ProtoReflect().Type().New().Interface()
		if err := proto.Unmarshal(encoded, decoded); err != nil {
			t.Fatal(err)
		}
		if err := validator.Validate(decoded); (err == nil) != tt.valid {
			t.Errorf("%T validity=%v: %v", decoded, tt.valid, err)
		}
	}
}

func TestHTTPAndOpenAPIContracts(t *testing.T) {
	service := userv1.File_user_v1_user_proto.Services().ByName("UserService")
	if service == nil || service.Methods().Len() != 2 {
		t.Fatal("expected two unary UserService methods")
	}
	tests := []struct{ name, verb, path, body string }{
		{"CreateUser", "post", "/v1/users", "*"},
		{"GetUser", "get", "/v1/users/{id}", ""},
	}
	var document struct {
		Swagger     string                                `json:"swagger"`
		Paths       map[string]map[string]json.RawMessage `json:"paths"`
		Definitions map[string]json.RawMessage            `json:"definitions"`
	}
	data, err := os.ReadFile("gen/openapi/api.swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.Swagger != "2.0" || len(document.Paths) != 3 {
		t.Fatal("expected merged OpenAPI 2.0 paths")
	}
	if len(document.Paths["/v1/order/{id}"]["get"]) == 0 {
		t.Fatal("merged spec is missing OrderService")
	}
	for _, name := range []string{"v1User", "v1CreateUserRequest", "v1CreateUserResponse", "v1GetUserResponse"} {
		if len(document.Definitions[name]) == 0 {
			t.Errorf("missing OpenAPI message schema %s", name)
		}
	}
	for _, tt := range tests {
		method := service.Methods().ByName(protoreflect.Name(tt.name))
		if method == nil || method.IsStreamingClient() || method.IsStreamingServer() {
			t.Fatalf("%s must be unary", tt.name)
		}
		rule := proto.GetExtension(method.Options(), annotations.E_Http).(*annotations.HttpRule)
		path := rule.GetGet()
		if tt.verb == "post" {
			path = rule.GetPost()
		}
		if path != tt.path || rule.GetBody() != tt.body {
			t.Errorf("%s HTTP binding = %v", tt.name, rule)
		}
		if len(document.Paths[tt.path][tt.verb]) == 0 {
			t.Errorf("missing OpenAPI %s %s", tt.verb, tt.path)
		}
	}
}
