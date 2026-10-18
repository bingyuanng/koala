package config

import (
	"errors"
	koala "github.com/bingyuanng/koala"
	"github.com/bingyuanng/koala/observability"
)

// Validate checks the combined framework template. Applications validate their
// own configuration using the component validators and application-specific rules.
func Validate(c Config) error {
	return errors.Join(
		koala.ValidateServerConfig(c.Server, c.Requests),
		observability.ValidateLogger(c.Logging),
		observability.ValidateTelemetry(c.Telemetry),
	)
}
