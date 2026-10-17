package cli

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"
)

// Version is replaced by release builds with -ldflags -X.
var Version = "dev"

const help = `Koala CLI

Usage:
  koala help
  koala version
  koala new <project> [--module path]
  koala new service <name>
  koala gen [--buf path] [--strict] [--local-plugins directory]
  koala config json|env [--output path] [--force]
  koala start [--gen]

`

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")) {
		if _, err := fmt.Fprint(stdout, help); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		if _, err := fmt.Fprintln(stdout, "koala", Version); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	switch args[0] {
	case "new", "gen", "generate", "config", "start":
		if err := operation(args, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code := exit.ExitCode()
				if code < 0 {
					return 128 + int(exit.Sys().(syscall.WaitStatus).Signal())
				}
				return code
			}
			return 1
		}
		return 0
	}
	fmt.Fprintf(stderr, "unsupported command %q; run koala help\n", args[0])
	return 2
}
