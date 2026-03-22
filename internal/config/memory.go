package config

import (
	"fmt"
	"os"
	"path/filepath"
)

const memoryFileName = "memory.md"

// ReadMemoryFile reads the memory.md file from the given session directory.
// Returns empty string if the file does not exist.
func ReadMemoryFile(sessionDir string) (string, error) {
	return ReadOptionalFile(filepath.Join(sessionDir, memoryFileName))
}

// WriteMemoryFile writes content to the memory.md file in the given session
// directory, creating it if it doesn't exist.
func WriteMemoryFile(sessionDir, content string) error {
	path := filepath.Join(sessionDir, memoryFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	return os.WriteFile(path, []byte(content), 0600)
}
