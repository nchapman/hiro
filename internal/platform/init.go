// Package platform handles initialization of the hiro platform root directory.
package platform

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/nchapman/hiro/internal/platform/fsperm"
)

//go:embed defaults/agents
var defaultAgents embed.FS

// requiredDirs are the top-level directories that must exist in the platform root.
var requiredDirs = []string{
	"agents",
	"config",
	"db",
	"instances",
	"mounts",
	"skills",
	"workspace",
}

// Init ensures the platform root directory structure exists and seeds default
// agent definitions if the platform is new. It is safe to call on an
// existing platform — it will not overwrite files that already exist.
func Init(dir string, logger *slog.Logger) error {
	for _, d := range requiredDirs {
		path := filepath.Join(dir, d)
		// config/ contains secrets — restrict to owner only.
		perm := fsperm.DirShared
		if d == "config" {
			perm = fsperm.DirPrivate
		}
		if err := os.MkdirAll(path, perm); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
		// MkdirAll won't tighten perms on existing dirs. Ensure config/ is
		// always restricted, even on upgrades from older installs.
		if d == "config" {
			if err := os.Chmod(path, fsperm.DirPrivate); err != nil {
				logger.Warn("failed to tighten config directory permissions", "error", err)
			}
		}
	}

	// Seed default agents if the agents directory is empty.
	agentsDir := filepath.Join(dir, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return fmt.Errorf("reading agents dir: %w", err)
	}
	if len(entries) > 0 {
		logger.Debug("agents directory non-empty, skipping defaults")
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
		rel := strings.TrimPrefix(path, "defaults/agents/")
		if rel == path {
			return nil
		}

		dest := filepath.Join(agentsDir, filepath.FromSlash(rel))

		if d.IsDir() {
			return os.MkdirAll(dest, fsperm.DirStandard)
		}

		data, err := defaultAgents.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading embedded %s: %w", path, err)
		}
		if err := os.WriteFile(dest, data, fsperm.FileStandard); err != nil {
			return fmt.Errorf("writing %s: %w", dest, err)
		}
		return nil
	})
}
