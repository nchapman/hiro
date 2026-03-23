package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/fantasy"

	"github.com/nchapman/hivebot/internal/config"
)

type useSkillInput struct {
	Name string `json:"name" description:"The name of the skill to activate."`
}

// buildSkillTool returns a use_skill tool that reads skills from the agent's
// current config. The config pointer ensures the tool always sees the latest
// skills (which are re-scanned from disk each turn).
//
// allowedDirs confines file reads to known skill directories (defense-in-depth).
// Skill paths are set by LoadSkills at scan time, so under normal operation they
// are always within these directories. The check guards against bugs or
// corruption that could place an unexpected path in SkillConfig.Path.
func buildSkillTool(cfg *config.AgentConfig, allowedDirs []string) fantasy.AgentTool {
	return fantasy.NewAgentTool("use_skill",
		"Activate a skill to get its full instructions and required formats. You MUST call this before performing any task that matches a skill — skills contain critical details that are not shown in the summary.",
		func(ctx context.Context, input useSkillInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.Name == "" {
				return fantasy.NewTextErrorResponse("skill name is required"), nil
			}

			// Find the skill by name in the current config
			var skill *config.SkillConfig
			for i := range cfg.Skills {
				if cfg.Skills[i].Name == input.Name {
					skill = &cfg.Skills[i]
					break
				}
			}
			if skill == nil {
				names := make([]string, len(cfg.Skills))
				for i, s := range cfg.Skills {
					names[i] = s.Name
				}
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("skill %q not found. Available: %s", input.Name, strings.Join(names, ", "))), nil
			}

			// Defense-in-depth: resolve symlinks before the confinement check.
			// Without this, a symlink inside skills/ pointing outside the
			// allowed tree would pass the string-prefix check.
			realPath := skill.Path
			if resolved, err := filepath.EvalSymlinks(skill.Path); err == nil {
				realPath = resolved
			}
			if !isUnderAllowedDir(realPath, allowedDirs) {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("skill %q path is outside allowed directories", input.Name)), nil
			}

			// Read and parse the skill file, returning only the body (no frontmatter)
			parsed, err := config.ParseMarkdownFile(realPath)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("error reading skill file: %v", err)), nil
			}

			var result strings.Builder
			result.WriteString(parsed.Body)

			// List bundled resources for directory skills only.
			// Directory skills have SKILL.md; flat skills (e.g. search.md) sit
			// alongside peers — listing the parent would expose unrelated files.
			if filepath.Base(realPath) == "SKILL.md" {
				skillDir := filepath.Dir(realPath)
				entries, err := os.ReadDir(skillDir)
				if err == nil {
					const maxResources = 50
					var resources []string
					truncated := false
					for _, e := range entries {
						if len(resources) >= maxResources {
							truncated = true
							break
						}
						name := e.Name()
						if name == "SKILL.md" {
							continue
						}
						if e.IsDir() {
							resources = append(resources, name+"/")
							subEntries, subErr := os.ReadDir(filepath.Join(skillDir, name))
							if subErr == nil {
								for _, sub := range subEntries {
									if len(resources) >= maxResources {
										truncated = true
										break
									}
									subName := name + "/" + sub.Name()
									if sub.IsDir() {
										subName += "/"
									}
									resources = append(resources, "  "+subName)
								}
							}
						} else {
							resources = append(resources, name)
						}
					}
					if truncated {
						resources = append(resources, "... (truncated)")
					}
					if len(resources) > 0 {
						result.WriteString("\n\n## Bundled Resources\n\n")
						for _, r := range resources {
							fmt.Fprintf(&result, "- %s\n", r)
						}
					}
				}
			}

			return fantasy.NewTextResponse(result.String()), nil
		},
	)
}

// isUnderAllowedDir reports whether path is under one of the allowed directories.
// Both path and each allowed dir are cleaned before comparison. If no non-empty
// dirs are provided, returns true (no confinement — used in tests with nil dirs).
func isUnderAllowedDir(path string, allowedDirs []string) bool {
	cleanPath := filepath.Clean(path)
	hasAny := false
	for _, dir := range allowedDirs {
		if dir == "" {
			continue
		}
		hasAny = true
		prefix := filepath.Clean(dir) + string(filepath.Separator)
		if strings.HasPrefix(cleanPath, prefix) {
			return true
		}
	}
	return !hasAny // no effective dirs = no confinement (tests)
}
