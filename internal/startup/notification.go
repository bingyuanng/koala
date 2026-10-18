// Package startup carries readiness information from an application to the CLI.
package startup

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// Environment identifies the inherited pipe used only by koala start. The
// leading underscore keeps this separate from application configuration keys.
const Environment = "_KOALA_STARTUP_FD"

type Notification struct {
	GRPC       string
	HTTP       string
	HealthPath string
	Docs       bool
}

// Notify is a no-op when the application runs without the development CLI.
func Notify(notification Notification) error {
	value, ok := os.LookupEnv(Environment)
	if !ok {
		return nil
	}
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 {
		return fmt.Errorf("invalid startup pipe descriptor")
	}
	pipe := os.NewFile(uintptr(fd), "koala-startup")
	if pipe == nil {
		return fmt.Errorf("invalid startup pipe")
	}
	defer pipe.Close()
	return json.NewEncoder(pipe).Encode(notification)
}
