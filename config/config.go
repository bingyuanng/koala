// Package config contains typed framework configuration and constructor defaults.
package config

// Config is the framework configuration. Durations use Go duration strings.
type Config struct {
	App       AppConfig       `json:"app"`
	Server    ServerConfig    `json:"server"`
	Requests  RequestConfig   `json:"requests"`
	Logging   LoggingConfig   `json:"logging"`
	Telemetry TelemetryConfig `json:"telemetry"`
}

type AppConfig struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Environment string `json:"environment"`
}

type ServerConfig struct {
	HTTP            HTTPConfig `json:"http"`
	GRPC            GRPCConfig `json:"grpc"`
	ShutdownTimeout string     `json:"shutdown_timeout"`
}

type HTTPConfig struct {
	Enabled        bool     `json:"enabled"`
	Port           int      `json:"port"`
	Host           string   `json:"host"`
	WriteTimeout   string   `json:"write_timeout"`
	MaxBodyBytes   int64    `json:"max_body_bytes"`
	AllowedOrigins []string `json:"allowed_origins"`
	Docs           bool     `json:"docs"`
}

type GRPCConfig struct {
	Port       int    `json:"port"`
	Host       string `json:"host"`
	CertFile   string `json:"cert_file"`
	KeyFile    string `json:"key_file"`
	CAFile     string `json:"ca_file"`
	ServerName string `json:"server_name"`
}

type RequestConfig struct {
	DefaultTimeout string            `json:"default_timeout"`
	MethodTimeouts map[string]string `json:"method_timeouts"`
}

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
		Telemetry: TelemetryConfig{Traces: TraceConfig{Enabled: true, Exporter: "stdout", SampleRatio: 1}},
	}
}

// LoggingConfig controls stdout JSON logging. Custom handlers are runtime options.
type LoggingConfig struct {
	Level string `json:"level"`
}
type TelemetryConfig struct {
	Traces  TraceConfig   `json:"traces"`
	Metrics MetricsConfig `json:"metrics"`
}
type TraceConfig struct {
	Enabled     bool    `json:"enabled"`
	Exporter    string  `json:"exporter"`
	SampleRatio float64 `json:"sample_ratio"`
	Endpoint    string  `json:"endpoint"`
	Insecure    bool    `json:"insecure"`
}
type MetricsConfig struct {
	Enabled  bool   `json:"enabled"`
	Endpoint string `json:"endpoint"`
	Insecure bool   `json:"insecure"`
}
