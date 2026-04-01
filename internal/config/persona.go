package config

import (
	"fmt"
	"os"
	"path/filepath"
)

const personaFileName = "persona.md"

// ReadPersonaFile reads the persona.md file from the given instance directory.
// Returns empty string if the file does not exist.
func ReadPersonaFile(instanceDir string) (string, error) {
	return ReadOptionalFile(filepath.Join(instanceDir, personaFileName))
}

// WritePersonaFile writes content to the persona.md file in the given instance
// directory, creating it if it doesn't exist. Uses atomic write (temp+rename)
// so concurrent readers never see partial content.
func WritePersonaFile(instanceDir, content string) error {
	path := filepath.Join(instanceDir, personaFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	return atomicWrite(path, []byte(content), 0600)
}
