package cli

import (
	"fmt"
	"io"
)

// Version is replaced by release builds with -ldflags -X.
var Version = "dev"

const help = `Koala foundation CLI

Usage:
  koala help
  koala version

Runtime, generation, and project creation commands are planned for later phases.
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
	fmt.Fprintf(stderr, "unsupported command %q; run koala help\n", args[0])
	return 2
}
