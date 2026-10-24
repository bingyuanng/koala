package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ResolveBinary resolves PATH executables, including mise shims, in the project.
func ResolveBinary(root, name string) (string, error) {
	binary, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	// Mise shims point to mise itself. Inspect the actual tool's build metadata
	// and pass its path to Buf, rather than inspecting or executing the shim.
	mise, miseErr := exec.LookPath("mise")
	if miseErr == nil {
		binaryInfo, binaryErr := os.Stat(binary)
		miseInfo, statErr := os.Stat(mise)
		if binaryErr == nil && statErr == nil && os.SameFile(binaryInfo, miseInfo) {
			cmd := exec.Command(mise, "which", name)
			cmd.Dir = root
			output, err := cmd.Output()
			if err != nil {
				return "", fmt.Errorf("resolve %s through mise: %w", name, err)
			}
			binary = strings.TrimSpace(string(output))
			if !filepath.IsAbs(binary) {
				return "", fmt.Errorf("mise which %s returned a non-absolute path %q", name, binary)
			}
		}
	}
	return filepath.Abs(binary)
}
