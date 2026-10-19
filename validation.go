package koala

import (
	"errors"
	"fmt"
	"time"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// ValidateConfig preflights policy names and validation rules against the static
// generated service descriptors, before application dependencies exist.
func ValidateConfig(server ServerConfig, requests RequestConfig, services ...protoreflect.ServiceDescriptor) error {
	if e := ValidateServerConfig(server, requests); e != nil {
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
	for method := range requests.MethodTimeouts {
		if !known[method] {
			return fmt.Errorf("koala: request timeout references unknown RPC %s", method)
		}
	}
	if _, e := protovalidate.New(protovalidate.WithMessages(messages...)); e != nil {
		return errors.New("koala: invalid request validation rules")
	}
	return nil
}

func (a *App) prepareRequests() (protovalidate.Validator, error) {
	budget, _ := time.ParseDuration(a.requests.DefaultTimeout)
	var messages []proto.Message
	for _, r := range a.registrations {
		for i := 0; i < r.Descriptor.Methods().Len(); i++ {
			m := r.Descriptor.Methods().Get(i)
			key := string(r.Descriptor.FullName()) + "/" + string(m.Name())
			methodBudget := budget
			if v, ok := a.requests.MethodTimeouts[key]; ok {
				methodBudget, _ = time.ParseDuration(v)
			}
			a.methods["/"+key] = methodBudget
			messages = append(messages, dynamicpb.NewMessage(m.Input()))
		}
	}
	for key := range a.requests.MethodTimeouts {
		if _, ok := a.methods["/"+key]; !ok {
			return nil, fmt.Errorf("koala: request timeout references unknown RPC %s", key)
		}
	}
	validator, err := protovalidate.New(protovalidate.WithMessages(messages...))
	if err != nil {
		return nil, errors.New("koala: invalid request validation rules")
	}
	return validator, nil
}
