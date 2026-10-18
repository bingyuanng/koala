package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDefaultConfigAndJSONOverrides(t *testing.T) {
	original := DefaultConfig()
	if original.Server.HTTP.Port != 8080 || !original.Server.HTTP.Enabled || original.Server.GRPC.Port != 9090 || original.Server.ShutdownTimeout != "15s" || original.Requests.DefaultTimeout != "5s" || original.Telemetry.Traces.Enabled {
		t.Fatalf("unexpected defaults: %+v", original)
	}
	cfg := DefaultConfig()
	if err := json.Unmarshal([]byte(`{"server":{"http":{"enabled":false,"port":0}}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.HTTP.Enabled || cfg.Server.HTTP.Port != 0 {
		t.Fatal("explicit false/zero did not overwrite defaults")
	}
	if cfg.Server.GRPC != original.Server.GRPC || cfg.Server.ShutdownTimeout != original.Server.ShutdownTimeout || cfg.Requests.DefaultTimeout != original.Requests.DefaultTimeout {
		t.Fatal("missing fields did not retain defaults")
	}
	if !reflect.DeepEqual(DefaultConfig(), original) {
		t.Fatal("default construction was affected by another config")
	}
	if err := json.Unmarshal([]byte(`{"server":{"http":{"port":"invalid"}}}`), &cfg); err == nil {
		t.Fatal("invalid JSON field type accepted")
	}
}
