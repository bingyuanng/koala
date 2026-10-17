package koala

import (
	"errors"
	"fmt"

	"buf.build/go/protovalidate"
	"github.com/bingyuanng/koala/config"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// ValidateConfig preflights policy names and validation rules against the static
// generated service descriptors. Generated bootstrap.ValidateConfig is suitable
// as config.Load's validation callback, before application dependencies exist.
func ValidateConfig(c config.Config, services ...protoreflect.ServiceDescriptor) error {
	if e := config.Validate(c); e != nil {
		return e
	}
	known := map[string]bool{}
	messages := []proto.Message{}
	for _, service := range services {
		if service == nil {
			return errors.New("koala: service descriptor is missing")
		}
		for i := 0; i < service.Methods().Len(); i++ {
			m := service.Methods().Get(i)
			if m.IsStreamingClient() || m.IsStreamingServer() {
				return fmt.Errorf("koala: streaming is unsupported: %s", m.FullName())
			}
			known[string(service.FullName())+"/"+string(m.Name())] = true
			messages = append(messages, dynamicpb.NewMessage(m.Input()))
		}
	}
	for method := range c.Requests.MethodTimeouts {
		if !known[method] {
			return fmt.Errorf("koala: request timeout references unknown RPC %s", method)
		}
	}
	if _, e := protovalidate.New(protovalidate.WithMessages(messages...)); e != nil {
		return errors.New("koala: invalid request validation rules")
	}
	return nil
}
