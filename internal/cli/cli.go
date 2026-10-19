package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
)

// Version is replaced by release builds with -ldflags -X.
var Version = "dev"

const help = `Koala CLI

Usage:
  koala help
  koala version
  koala new <project> [--module path] [--framework-version version]
  koala new service <name>
  koala gen [--buf path] [--strict] [--local-plugins directory]
  koala config <dev|stg|prd>
  koala config <dev|stg|prd> env
  koala start [--gen]

Requires mise on PATH: https://mise.jdx.dev/getting-started.html
New projects include mise.toml with pinned Go and Buf versions.

`

func helpText() string {
	if frameworkVersion() == "" {
		return strings.Replace(help, "[--framework-version version]", "[--framework-version version | --framework-path checkout]", 1)
	}
	return help
}

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")) {
		if _, err := fmt.Fprint(stdout, helpText()); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		version := Version
		if installed := frameworkVersion(); version == "dev" && installed != "" {
			version = installed
		}
		if _, err := fmt.Fprintln(stdout, "koala", version); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	switch args[0] {
	case "new", "gen", "generate", "config", "start":
		if _, err := exec.LookPath("mise"); err != nil {
			fmt.Fprintln(stderr, "koala: mise is required on PATH; install and activate it: https://mise.jdx.dev/getting-started.html")
			return 1
		}
		var err error
		switch args[0] {
		case "new":
			err = newCommand(args[1:], stdout, stderr)
		case "config":
			err = configCommand(args[1:], stdout)
		default:
			err = generateCommand(args[0], args[1:], stdout, stderr)
		}
		if err != nil {
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

func flags(name string, out io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(out)
	return f
}
