package observability

import (
	"fmt"
	"math"
	"net/url"
)

// ServiceConfig identifies the application in telemetry resource attributes.
type ServiceConfig struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Environment string `json:"environment"`
}

// LoggerConfig controls JSON logging. Applications may also supply custom handlers.
type LoggerConfig struct {
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

// ValidateLogger checks application logging settings.
func ValidateLogger(c LoggerConfig) error {
	switch c.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: unsupported logging level")
	}
	return nil
}

// ValidateTelemetry checks exporter and sampling settings without printing values.
func ValidateTelemetry(c TelemetryConfig) error {
	traces := c.Traces
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
	if c.Metrics.Enabled && c.Metrics.Endpoint == "" {
		return fmt.Errorf("config: OTLP metrics endpoint is required")
	}
	for _, endpoint := range []struct {
		enabled  bool
		url      string
		insecure bool
	}{{traces.Enabled && traces.Exporter == "otlp", traces.Endpoint, traces.Insecure}, {c.Metrics.Enabled, c.Metrics.Endpoint, c.Metrics.Insecure}} {
		if endpoint.enabled {
			u, e := url.Parse(endpoint.url)
			if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && endpoint.insecure)) {
				return fmt.Errorf("config: OTLP endpoint must be an HTTPS URL, or HTTP with insecure explicitly enabled; credentials and query strings are not allowed")
			}
		}
	}
	return nil
}
