package config

import (
	"fmt"
	"os"
	"path/filepath"
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	return atomicWrite(path, []byte(content), 0o600)
}

// atomicWrite writes content to path via a temp file + rename.
func atomicWrite(path string, content []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".hiro-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := f.Chmod(mode); err != nil {
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
	return os.Rename(tmp, path)
}
