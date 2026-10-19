package cli

import (
	"errors"
	"flag"
	"io"
)

func flags(name string, out io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(out)
	return f
}

func operation(args []string, out, errout io.Writer) error {
	switch args[0] {
	case "new":
		return newCommand(args[1:], out, errout)
	case "config":
		return configCommand(args[1:], out, errout)
	case "gen", "generate", "start":
		return generateCommand(args[0], args[1:], out, errout)
	default:
		return errors.New("unsupported operation")
	}
}
