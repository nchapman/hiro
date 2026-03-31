package inference

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"charm.land/fantasy"

	"github.com/nchapman/hivebot/internal/config"
)

func buildSkillTool(cfg *config.AgentConfig, allowedDirs []string, logger *slog.Logger) fantasy.AgentTool {
	return fantasy.NewAgentTool("use_skill",
		"Load a skill's full instructions. Call before performing a skill-matched task.",
		func(ctx context.Context, input struct {
			Name string `json:"name" description:"The name of the skill to activate."`
		}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.Name == "" {
				return fantasy.NewTextErrorResponse("skill name is required"), nil
			}

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

			realPath := skill.Path
			if resolved, err := filepath.EvalSymlinks(skill.Path); err == nil {
				realPath = resolved
			}
			if !isUnderAllowedDir(realPath, allowedDirs) {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("skill %q path is outside allowed directories", input.Name)), nil
			}

			logger.Info("tool call", "tool", "use_skill", "skill", input.Name)

			parsed, err := config.ParseMarkdownFile(realPath)
			if err != nil {
				logger.Warn("use_skill read failed", "skill", input.Name, "error", err)
				return fantasy.NewTextErrorResponse(fmt.Sprintf("error reading skill file: %v", err)), nil
			}

			var result strings.Builder
			result.WriteString(parsed.Body)

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
	return !hasAny
}
