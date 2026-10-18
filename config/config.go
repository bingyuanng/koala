// Package config loads application-defined configuration from JSON and environment
// variables. It also provides a combined component template for CLI output.
package config

import (
	koala "github.com/bingyuanng/koala"
	"github.com/bingyuanng/koala/observability"
)

// Config is the combined component template used by the CLI. Applications own
// their final configuration type; runtime components accept individual sections.
type Config struct {
	App       AppConfig       `json:"app"`
	Server    ServerConfig    `json:"server"`
	Requests  RequestConfig   `json:"requests"`
	Logging   LoggingConfig   `json:"logging"`
	Telemetry TelemetryConfig `json:"telemetry"`
}

// Component type aliases keep the combined template compatible with component APIs.
type AppConfig = observability.ServiceConfig
type ServerConfig = koala.ServerConfig
type HTTPConfig = koala.HTTPConfig
type GRPCConfig = koala.GRPCConfig
type RequestConfig = koala.RequestConfig
type LoggingConfig = observability.LoggerConfig
type TelemetryConfig = observability.TelemetryConfig
type TraceConfig = observability.TraceConfig
type MetricsConfig = observability.MetricsConfig

// DefaultConfig returns a fresh configuration with framework defaults.
// Callers can decode JSON into it before validating the resulting values.
func DefaultConfig() Config {
	return Config{
		App: AppConfig{Version: "dev", Environment: "development"},
		Server: ServerConfig{
			HTTP:            HTTPConfig{Enabled: true, Port: 8080, Host: "127.0.0.1", WriteTimeout: "0s", MaxBodyBytes: 4 << 20, Docs: true, AllowedOrigins: []string{}},
			GRPC:            GRPCConfig{Port: 9090, Host: "127.0.0.1"},
			ShutdownTimeout: "15s",
		},
		Requests:  RequestConfig{DefaultTimeout: "5s", MethodTimeouts: map[string]string{}},
		Logging:   LoggingConfig{Level: "info"},
		Telemetry: TelemetryConfig{Traces: TraceConfig{Enabled: false, Exporter: "stdout", SampleRatio: 1}},
	}
}
