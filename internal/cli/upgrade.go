package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

func upgradeCommand(args []string, out, errout io.Writer) error {
	f := flags("upgrade", errout)
	f.Usage = func() {
		fmt.Fprintln(errout, "Usage: koala upgrade\n\nInstall the latest Koala CLI. Alias: koala update.")
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("usage: koala upgrade")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	return upgradeExecutable(executable, out, errout)
}

func upgradeExecutable(executable string, out, errout io.Writer) error {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		return errors.New("koala: Go is required to upgrade the CLI; install it with mise use -g go@1.26.10")
	}
	// Stage beside the executable so the final rename stays on the same filesystem.
	// A failed download or build leaves the installed CLI untouched.
	dir, err := os.MkdirTemp(filepath.Dir(executable), ".koala-upgrade-")
	if err != nil {
		return fmt.Errorf("prepare CLI upgrade in %s: %w", filepath.Dir(executable), err)
	}
	defer os.RemoveAll(dir)
	fmt.Fprintln(out, "Installing the latest Koala CLI...")
	cmd := exec.Command(goBinary, "install", "github.com/bingyuanng/koala/cmd/koala@latest")
	cmd.Env = append(os.Environ(), "GOBIN="+dir)
	cmd.Stdout = out
	cmd.Stderr = errout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("upgrade Koala CLI: %w", err)
	}
	if err := os.Rename(filepath.Join(dir, "koala"), executable); err != nil {
		return fmt.Errorf("replace Koala CLI at %s: %w", executable, err)
	}
	fmt.Fprintf(out, "Upgraded Koala CLI at %s. Run koala version to check the installed version.\n", executable)
	return nil
}
