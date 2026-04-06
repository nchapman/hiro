package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nchapman/hiro/internal/platform/fsperm"
)

const memoryFileName = "memory.md"

// ReadMemoryFile reads the memory.md file from the given instance directory.
// Returns empty string if the file does not exist.
func ReadMemoryFile(instanceDir string) (string, error) {
	return ReadOptionalFile(filepath.Join(instanceDir, memoryFileName))
}

// WriteMemoryFile writes content to the memory.md file in the given instance
// directory, creating it if it doesn't exist. Uses atomic write (temp+rename)
// so concurrent readers never see partial content.
func WriteMemoryFile(instanceDir, content string) error {
	path := filepath.Join(instanceDir, memoryFileName)
	if err := os.MkdirAll(filepath.Dir(path), fsperm.DirPrivate); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	return atomicWrite(path, []byte(content))
}

// atomicWrite writes content to path via a temp file + rename.
// Files are always created with fsperm.FilePrivate (0600).
func atomicWrite(path string, content []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".hiro-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := f.Chmod(fsperm.FilePrivate); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
