// Package workspace handles initialization of the hive workspace directory.
package workspace

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed defaults/agents
var defaultAgents embed.FS

// requiredDirs are the top-level directories that must exist in a workspace.
var requiredDirs = []string{
	"agents",
	"sessions",
	"skills",
}

// coordinatorDirs are directories owned by the hive-coordinators group.
// Coordinator-mode agents get write access; others get read-only via "other" bits.
var coordinatorDirs = map[string]bool{
	"agents": true,
	"skills": true,
}

// Init ensures the workspace directory structure exists and seeds default
// agent definitions if the workspace is new. It is safe to call on an
// existing workspace — it will not overwrite files that already exist.
func Init(dir string, logger *slog.Logger) error {
	// Detect hive-coordinators group for directory ownership.
	coordGID := lookupGroupGID("hive-coordinators")

	for _, d := range requiredDirs {
		path := filepath.Join(dir, d)
		if err := os.MkdirAll(path, 0775); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
		// agents/ and skills/ are owned by hive-coordinators with setgid,
		// so coordinator agents can write and others get read-only access.
		// Also walk existing subdirectories to handle upgrades from
		// pre-coordinator versions where dirs were owned by root.
		if coordGID >= 0 && coordinatorDirs[d] {
			if err := applyCoordinatorOwnership(path, coordGID, logger); err != nil {
				logger.Warn("failed to apply coordinator ownership", "dir", d, "error", err)
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
		logger.Debug("workspace already has agents, skipping defaults")
		return nil
	}

	logger.Info("seeding default agent definitions")
	return seedDefaults(agentsDir, coordGID)
}

// seedDefaults copies embedded default agent definitions into the workspace.
func seedDefaults(agentsDir string, coordGID int) error {
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
			if err := os.MkdirAll(dest, 0755); err != nil {
				return err
			}
			// Setgid + group-writable so coordinator agents can
			// create new files inside seeded agent directories.
			if coordGID >= 0 {
				if err := os.Chown(dest, -1, coordGID); err != nil {
					return fmt.Errorf("chown %s to hive-coordinators: %w", rel, err)
				}
				if err := os.Chmod(dest, 02775); err != nil {
					return fmt.Errorf("chmod %s: %w", rel, err)
				}
			}
			return nil
		}

		data, err := defaultAgents.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading embedded %s: %w", path, err)
		}
		// Seeded files are 0644 (not group-writable) intentionally —
		// coordinators can create new agents but cannot rewrite the
		// shipped defaults, preventing prompt injection persistence.
		if err := os.WriteFile(dest, data, 0644); err != nil {
			return fmt.Errorf("writing %s: %w", dest, err)
		}
		return nil
	})
}

// applyCoordinatorOwnership sets hive-coordinators group and setgid on a
// directory and all its subdirectories. Files are left as-is (seeded defaults
// stay root-owned 0644 to prevent prompt injection persistence; new files
// created by coordinators inherit the group via setgid).
func applyCoordinatorOwnership(root string, coordGID int, logger *slog.Logger) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if err := os.Chown(path, -1, coordGID); err != nil {
			logger.Warn("cannot chown directory to hive-coordinators", "path", path, "error", err)
			return nil // best-effort
		}
		if err := os.Chmod(path, 02775); err != nil {
			logger.Warn("cannot set setgid on directory", "path", path, "error", err)
		}
		return nil
	})
}

// lookupGroupGID returns the GID of the named group, or -1 if not found.
func lookupGroupGID(name string) int {
	grp, err := user.LookupGroup(name)
	if err != nil {
		return -1
	}
	gid, err := strconv.Atoi(grp.Gid)
	if err != nil {
		return -1
	}
	return gid
}
