package config

import "testing"

func TestDefaultConfig(t *testing.T) {
	original := DefaultConfig()
	if original.Server.HTTP.Port != 8080 || !original.Server.HTTP.Enabled || original.Server.GRPC.Port != 9090 || original.Server.ShutdownTimeout != "15s" || original.Requests.DefaultTimeout != "5s" || original.Telemetry.Traces.Enabled {
		t.Fatalf("unexpected defaults: %+v", original)
	}
	modified := DefaultConfig()
	modified.Requests.MethodTimeouts["example.Service/Get"] = "1s"
	if len(DefaultConfig().Requests.MethodTimeouts) != 0 {
		t.Fatal("default configurations share a mutable timeout map")
	}
}
