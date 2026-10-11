package compatibility

import (
	"context"
	"testing"

	"buf.build/go/protovalidate"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestPinnedAPIs(t *testing.T) {
	validator, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	msg := &emptypb.Empty{}
	if err := validator.Validate(msg); err != nil {
		t.Fatal(err)
	}
	if _, err := proto.Marshal(msg); err != nil {
		t.Fatal(err)
	}
	if runtime.NewServeMux() == nil {
		t.Fatal("nil gateway mux")
	}
	server := grpc.NewServer()
	server.Stop()
	_, span := otel.Tracer("koala.compatibility").Start(context.Background(), "fixture")
	span.End()
}
