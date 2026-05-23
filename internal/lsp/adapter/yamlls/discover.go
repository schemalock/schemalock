package yamlls

import (
	"os"
	"os/exec"
	"path/filepath"
)

// Resolve returns the absolute path to the yaml-language-server binary. The
// resolution order is:
//
//  1. If explicitPath is non-empty, verify it exists and is executable; return
//     it if so (or "" if the file is missing or not executable).
//  2. Search PATH for "yaml-language-server".
//  3. Return "" (not found).
//
// Missing yaml-ls is not an error: the caller logs a warning and starts
// without yaml-ls features. There is no error return because the absence of
// yaml-ls is an expected, documented degraded state.
func Resolve(explicitPath string) string {
	if explicitPath != "" {
		return resolveExplicit(explicitPath)
	}
	return resolveFromPATH()
}

// resolveExplicit checks whether path exists and is executable.
// Returns the cleaned absolute path, or "" if the file is missing/not
// executable.
func resolveExplicit(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	info, err := os.Stat(abs)
	if err != nil {
		return ""
	}
	// On Unix, check the execute bit. On Windows all files are "executable"
	// by extension; we leave the check to exec.Command at runtime.
	if info.Mode()&0o111 == 0 {
		return ""
	}
	return abs
}

// resolveFromPATH searches os.Getenv("PATH") for "yaml-language-server".
// Returns the resolved absolute path, or "" if not found.
func resolveFromPATH() string {
	found, err := exec.LookPath("yaml-language-server")
	if err != nil {
		return ""
	}
	return found
}
