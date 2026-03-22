// Package workspace handles initialization of the hive workspace directory.
package workspace

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

//go:embed defaults/agents
var defaultAgents embed.FS

// requiredDirs are the top-level directories that must exist in a workspace.
var requiredDirs = []string{
	"agents",
	"instances",
	"skills",
}

// Init ensures the workspace directory structure exists and seeds default
// agent definitions if the workspace is new. It is safe to call on an
// existing workspace — it will not overwrite files that already exist.
func Init(dir string, logger *slog.Logger) error {
	for _, d := range requiredDirs {
		path := filepath.Join(dir, d)
		if err := os.MkdirAll(path, 0755); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
	}

	// Seed default agents if the agents directory is empty.
	agentsDir := filepath.Join(dir, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return fmt.Errorf("reading agents dir: %w", err)
	}
	if len(entries) > 0 {
		logger.Debug("workspace already has agents, skipping defaults")
		return nil
	}

	logger.Info("seeding default agent definitions")
	return seedDefaults(agentsDir)
}

// seedDefaults copies embedded default agent definitions into the workspace.
func seedDefaults(agentsDir string) error {
	return fs.WalkDir(defaultAgents, "defaults/agents", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Strip the "defaults/agents/" prefix to get the relative path.
		// embed.FS always uses forward slashes, so use strings.TrimPrefix
		// and filepath.FromSlash for cross-platform correctness.
		rel := strings.TrimPrefix(path, "defaults/agents/")
		if rel == path {
			return nil
		}

		dest := filepath.Join(agentsDir, filepath.FromSlash(rel))

		if d.IsDir() {
			return os.MkdirAll(dest, 0755)
		}

		data, err := defaultAgents.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading embedded %s: %w", path, err)
		}
		if err := os.WriteFile(dest, data, 0644); err != nil {
			return fmt.Errorf("writing %s: %w", dest, err)
		}
		return nil
	})
}
