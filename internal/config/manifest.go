package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Manifest describes a running agent session persisted to disk.
type Manifest struct {
	ID        string    `yaml:"id"`
	Agent     string    `yaml:"agent"` // definition name (directory under agents/)
	Mode      AgentMode `yaml:"mode"`
	ParentID  string    `yaml:"parent_id,omitempty"`
	CreatedAt time.Time `yaml:"created_at"`
}

// WriteManifest writes a manifest to the given path as YAML.
func WriteManifest(path string, m Manifest) error {
	data, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// ReadManifest reads a manifest from the given path.
func ReadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}
