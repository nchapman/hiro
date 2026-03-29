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
// directory, creating it if it doesn't exist.
func WriteMemoryFile(instanceDir, content string) error {
	path := filepath.Join(instanceDir, memoryFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	return os.WriteFile(path, []byte(content), 0600)
}
