package inference

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/config"
)

//go:embed skill.md
var skillDescription string

// SkillExpander is called when a skill with allowed_tools is activated.
// It expands the session's tool set with the skill's tools.
type SkillExpander func(skill *config.SkillConfig) error

func buildSkillTool(cfg *config.AgentConfig, allowedDirs []string, onExpand SkillExpander, logger *slog.Logger) fantasy.AgentTool {
	return fantasy.NewAgentTool("Skill",
		skillDescription,
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

			// Expand session tools if the skill declares allowed_tools.
			if onExpand != nil && len(skill.AllowedTools) > 0 {
				if err := onExpand(skill); err != nil {
					logger.Warn("skill tool expansion failed", "skill", input.Name, "error", err)
					// Non-fatal: the skill instructions are still useful even
					// if tool expansion fails.
				}
			}

			logger.Info("tool call", "tool", "Skill", "skill", input.Name)

			parsed, err := config.ParseMarkdownFile(realPath)
			if err != nil {
				logger.Warn("Skill read failed", "skill", input.Name, "error", err)
				return fantasy.NewTextErrorResponse(fmt.Sprintf("error reading skill file: %v", err)), nil
			}

			var result strings.Builder
			result.WriteString(parsed.Body)

			if filepath.Base(realPath) == "SKILL.md" {
				appendBundledResources(&result, filepath.Dir(realPath))
			}

			return fantasy.NewTextResponse(result.String()), nil
		},
	)
}

// appendBundledResources lists the files in a directory skill's folder
// and appends them as a "Bundled Resources" section to the builder.
func appendBundledResources(result *strings.Builder, skillDir string) {
	entries, err := os.ReadDir(skillDir)
	if err != nil {
		return
	}
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
			resources, truncated = appendSubdirEntries(resources, skillDir, name, maxResources, truncated)
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
			fmt.Fprintf(result, "- %s\n", r)
		}
	}
}

// appendSubdirEntries adds entries from a subdirectory to the resource list.
func appendSubdirEntries(resources []string, skillDir, dirName string, maxResources int, truncated bool) ([]string, bool) {
	subEntries, err := os.ReadDir(filepath.Join(skillDir, dirName))
	if err != nil {
		return resources, truncated
	}
	for _, sub := range subEntries {
		if len(resources) >= maxResources {
			return resources, true
		}
		subName := dirName + "/" + sub.Name()
		if sub.IsDir() {
			subName += "/"
		}
		resources = append(resources, "  "+subName)
	}
	return resources, truncated
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
