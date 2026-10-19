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
	_, _, err := prepareRequests(requests, services)
	return err
}

func prepareRequests(requests RequestConfig, services []protoreflect.ServiceDescriptor) (map[string]time.Duration, protovalidate.Validator, error) {
	budget, _ := time.ParseDuration(requests.DefaultTimeout)
	methods := map[string]time.Duration{}
	var messages []proto.Message
	for _, service := range services {
		if service == nil {
			return nil, nil, errors.New("koala: service descriptor is missing")
		}
		for i := 0; i < service.Methods().Len(); i++ {
			m := service.Methods().Get(i)
			if m.IsStreamingClient() || m.IsStreamingServer() {
				return nil, nil, fmt.Errorf("koala: streaming is unsupported: %s", m.FullName())
			}
			methods["/"+string(service.FullName())+"/"+string(m.Name())] = budget
			messages = append(messages, dynamicpb.NewMessage(m.Input()))
		}
	}
	for method, timeout := range requests.MethodTimeouts {
		if _, known := methods["/"+method]; !known {
			return nil, nil, fmt.Errorf("koala: request timeout references unknown RPC %s", method)
		}
		methods["/"+method], _ = time.ParseDuration(timeout)
	}
	validator, err := protovalidate.New(protovalidate.WithMessages(messages...))
	if err != nil {
		return nil, nil, errors.New("koala: invalid request validation rules")
	}
	return methods, validator, nil
}
