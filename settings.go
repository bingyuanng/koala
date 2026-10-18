package koala

import (
	"fmt"
	"time"
)

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

// ValidateServerConfig checks transport settings and request budgets.
// ValidateConfig also checks method names against registered descriptors.
func ValidateServerConfig(server ServerConfig, requests RequestConfig) error {
	for name, port := range map[string]int{"server.grpc.port": server.GRPC.Port, "server.http.port": server.HTTP.Port} {
		if port < 0 || port > 65535 {
			return fmt.Errorf("config: %s must be a valid port", name)
		}
	}
	shutdown, err := time.ParseDuration(server.ShutdownTimeout)
	if err != nil || shutdown <= 0 {
		return fmt.Errorf("config: server.shutdown_timeout must be positive")
	}
	maxBudget, err := time.ParseDuration(requests.DefaultTimeout)
	if err != nil || maxBudget <= 0 {
		return fmt.Errorf("config: requests.default_timeout must be positive")
	}
	for _, s := range requests.MethodTimeouts {
		d, e := time.ParseDuration(s)
		if e != nil || d <= 0 {
			return fmt.Errorf("config: method timeouts must be positive durations")
		}
		if d > maxBudget {
			maxBudget = d
		}
	}
	write, err := time.ParseDuration(server.HTTP.WriteTimeout)
	if err != nil || write < 0 || (write > 0 && write <= maxBudget) {
		return fmt.Errorf("config: HTTP write timeout must be zero or greater than every request budget")
	}
	if server.HTTP.MaxBodyBytes <= 0 {
		return fmt.Errorf("config: HTTP max body bytes must be positive")
	}
	if (server.GRPC.CertFile == "") != (server.GRPC.KeyFile == "") {
		return fmt.Errorf("config: both TLS certificate and key are required")
	}
	return nil
}
