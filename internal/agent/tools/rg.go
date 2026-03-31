package tools

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"sync"
)

var findRg = sync.OnceValue(func() string {
	path, err := exec.LookPath("rg")
	if err != nil {
		return ""
	}
	return path
})

// rgExcludeGlobs builds ripgrep --glob exclusions from the shared excludedDirs.
var rgExcludeGlobs = func() []string {
	globs := make([]string, 0, len(excludedDirs))
	for dir := range excludedDirs {
		globs = append(globs, "!"+dir)
	}
	sort.Strings(globs)
	return globs
}()

// rgGlobCmd builds a ripgrep command for file glob matching (--files mode).
// Does not follow symlinks. Excludes hidden files and common noisy dirs.
func rgGlobCmd(ctx context.Context, globPattern string) *exec.Cmd {
	name := findRg()
	if name == "" {
		return nil
	}
	args := []string{"--files", "--null", "--no-ignore-vcs"}
	for _, ex := range rgExcludeGlobs {
		args = append(args, "--glob", ex)
	}
	args = append(args, "--glob", "!.*")
	if globPattern != "" {
		args = append(args, "--glob", globPattern)
	}
	return exec.CommandContext(ctx, name, args...)
}


// errRgUnavailable is returned when ripgrep is not installed.
var errRgUnavailable = fmt.Errorf("ripgrep not available")

// isRgUnavailable reports whether err indicates ripgrep is missing,
// as opposed to a real search error.
func isRgUnavailable(err error) bool {
	return errors.Is(err, errRgUnavailable) || errors.Is(err, exec.ErrNotFound)
}
