package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/bingyuanng/koala/config"
)

func configCommand(args []string, out, errout io.Writer) error {
	if len(args) == 0 || (args[0] != "json" && args[0] != "env") {
		return errors.New("usage: koala config json [--output path] [--force] | koala config env <environment>")
	}
	if args[0] == "env" {
		if len(args) == 2 && args[1] == "json" {
			return configEnvJSONCommand(out)
		}
		return configEnvCommand(args[1:], out)
	}
	f := flags("config", errout)
	output := f.String("output", "", "output file")
	force := f.Bool("force", false, "replace output file")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unexpected config arguments")
	}
	b, e := json.MarshalIndent(config.DefaultConfig(), "", "  ")
	b = append(b, '\n')
	if e != nil {
		return e
	}
	if *output == "" {
		_, e = out.Write(b)
		return e
	}
	if info, err := os.Lstat(*output); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing symlink output")
	}
	mode := os.O_CREATE | os.O_WRONLY | os.O_EXCL
	if *force {
		mode = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	fout, e := os.OpenFile(*output, mode, 0600)
	if e != nil {
		return e
	}
	_, e = fout.Write(b)
	return errors.Join(e, fout.Close())
}
