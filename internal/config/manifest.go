package config

import (
	"encoding/json"
	"os"
	"time"
)

// Manifest describes a running agent instance persisted to disk.
type Manifest struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent"` // definition name (directory under agents/)
	Mode      AgentMode `json:"mode"`
	ParentID  string    `json:"parent_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// WriteManifest writes a manifest to the given path as JSON.
func WriteManifest(path string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
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
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}
