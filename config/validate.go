package config

import (
	"fmt"
	"math"
	"net/url"
	"time"
)

// Validate checks framework configuration without printing supplied values.
// Runtime registration additionally checks method names against its descriptors.
func Validate(c Config) error {
	for name, port := range map[string]int{"server.grpc.port": c.Server.GRPC.Port, "server.http.port": c.Server.HTTP.Port} {
		if port < 0 || port > 65535 {
			return fmt.Errorf("config: %s must be a valid port", name)
		}
	}
	shutdown, err := time.ParseDuration(c.Server.ShutdownTimeout)
	if err != nil || shutdown <= 0 {
		return fmt.Errorf("config: server.shutdown_timeout must be positive")
	}
	maxBudget, err := time.ParseDuration(c.Requests.DefaultTimeout)
	if err != nil || maxBudget <= 0 {
		return fmt.Errorf("config: requests.default_timeout must be positive")
	}
	for _, s := range c.Requests.MethodTimeouts {
		d, e := time.ParseDuration(s)
		if e != nil || d <= 0 {
			return fmt.Errorf("config: method timeouts must be positive durations")
		}
		if d > maxBudget {
			maxBudget = d
		}
	}
	write, err := time.ParseDuration(c.Server.HTTP.WriteTimeout)
	if err != nil || write < 0 || (write > 0 && write <= maxBudget) {
		return fmt.Errorf("config: HTTP write timeout must be zero or greater than every request budget")
	}
	if c.Server.HTTP.MaxBodyBytes <= 0 {
		return fmt.Errorf("config: HTTP max body bytes must be positive")
	}
	if (c.Server.GRPC.CertFile == "") != (c.Server.GRPC.KeyFile == "") {
		return fmt.Errorf("config: both TLS certificate and key are required")
	}
	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: unsupported logging level")
	}
	traces := c.Telemetry.Traces
	if math.IsNaN(traces.SampleRatio) || traces.SampleRatio < 0 || traces.SampleRatio > 1 {
		return fmt.Errorf("config: sample_ratio must be between zero and one")
	}
	switch traces.Exporter {
	case "stdout", "otlp", "none":
	default:
		return fmt.Errorf("config: unsupported trace exporter")
	}
	if traces.Enabled && traces.Exporter == "otlp" && traces.Endpoint == "" {
		return fmt.Errorf("config: OTLP traces endpoint is required")
	}
	if c.Telemetry.Metrics.Enabled && c.Telemetry.Metrics.Endpoint == "" {
		return fmt.Errorf("config: OTLP metrics endpoint is required")
	}
	for _, endpoint := range []struct {
		enabled  bool
		url      string
		insecure bool
	}{{traces.Enabled && traces.Exporter == "otlp", traces.Endpoint, traces.Insecure}, {c.Telemetry.Metrics.Enabled, c.Telemetry.Metrics.Endpoint, c.Telemetry.Metrics.Insecure}} {
		if endpoint.enabled {
			u, e := url.Parse(endpoint.url)
			if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && endpoint.insecure)) {
				return fmt.Errorf("config: OTLP endpoint must be an HTTPS URL, or HTTP with insecure explicitly enabled; credentials and query strings are not allowed")
			}
		}
	}
	return nil
}
