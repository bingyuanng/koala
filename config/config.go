// Package config contains typed framework configuration and constructor defaults.
package config

// Config is the initial typed foundation, extended in Phase 2.
// Durations use strings in this prototype; the loader will parse them explicitly.
type Config struct {
	App      AppConfig     `json:"app"`
	Server   ServerConfig  `json:"server"`
	Requests RequestConfig `json:"requests"`
}

type AppConfig struct {
	Name string `json:"name"`
}

type ServerConfig struct {
	HTTP            HTTPConfig `json:"http"`
	GRPC            GRPCConfig `json:"grpc"`
	ShutdownTimeout string     `json:"shutdown_timeout"`
}

type HTTPConfig struct {
	Enabled bool `json:"enabled"`
	Port    int  `json:"port"`
}

type GRPCConfig struct {
	Port int `json:"port"`
}

type RequestConfig struct {
	DefaultTimeout string            `json:"default_timeout"`
	MethodTimeouts map[string]string `json:"method_timeouts"`
}

// DefaultConfig returns a fresh configuration with framework defaults.
// Callers can decode JSON into it before validating the resulting values.
func DefaultConfig() Config {
	return Config{
		Server: ServerConfig{
			HTTP:            HTTPConfig{Enabled: true, Port: 8080},
			GRPC:            GRPCConfig{Port: 9090},
			ShutdownTimeout: "15s",
		},
		Requests: RequestConfig{DefaultTimeout: "5s"},
	}
}
