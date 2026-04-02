// Package config handles parsing markdown files with YAML frontmatter
// into agent configurations, skill definitions, and other Hiro types.
package config

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Frontmatter represents the YAML metadata at the top of a markdown file.
type Frontmatter map[string]any

// ParsedMarkdown holds the result of parsing a markdown file with frontmatter.
type ParsedMarkdown struct {
	Frontmatter Frontmatter
	Body        string
}

// String returns a frontmatter value as a string, or empty if missing/wrong type.
func (f Frontmatter) String(key string) string {
	v, ok := f[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// StringSlice returns a frontmatter value as a []string, or nil if missing/wrong type.
// YAML unmarshals [a, b, c] as []any, so we convert each element.
func (f Frontmatter) StringSlice(key string) []string {
	v, ok := f[key]
	if !ok {
		return nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// StringMap returns a frontmatter value as a map[string]string, or nil if missing/wrong type.
func (f Frontmatter) StringMap(key string) map[string]string {
	v, ok := f[key]
	if !ok {
		return nil
	}
	// YAML v3 unmarshals nested maps as the same named type (Frontmatter),
	// so we need to check for both Frontmatter and plain map[string]any.
	var m map[string]any
	switch tv := v.(type) {
	case Frontmatter:
		m = tv
	case map[string]any:
		m = tv
	default:
		return nil
	}
	result := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			result[k] = s
		}
	}
	return result
}

// ParseMarkdown parses a markdown file with optional YAML frontmatter.
// Frontmatter is delimited by --- on its own line at the start of the file.
func ParseMarkdown(r io.Reader) (ParsedMarkdown, error) {
	scanner := bufio.NewScanner(r)
	var result ParsedMarkdown

	// Check for frontmatter delimiter
	if !scanner.Scan() {
		return result, scanner.Err()
	}
	firstLine := scanner.Text()

	if strings.TrimSpace(firstLine) != "---" {
		// No frontmatter — entire content is body
		var body strings.Builder
		body.WriteString(firstLine)
		body.WriteString("\n")
		for scanner.Scan() {
			body.WriteString(scanner.Text())
			body.WriteString("\n")
		}
		result.Body = strings.TrimSpace(body.String())
		return result, scanner.Err()
	}

	// Read frontmatter until closing ---
	var fmBuf bytes.Buffer
	closed := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		fmBuf.WriteString(line)
		fmBuf.WriteString("\n")
	}
	if !closed {
		return result, fmt.Errorf("unclosed frontmatter: missing closing ---")
	}

	// Parse YAML
	fm := make(Frontmatter)
	if err := yaml.Unmarshal(fmBuf.Bytes(), &fm); err != nil {
		return result, fmt.Errorf("invalid frontmatter YAML: %w", err)
	}
	result.Frontmatter = fm

	// Read body
	var body strings.Builder
	for scanner.Scan() {
		body.WriteString(scanner.Text())
		body.WriteString("\n")
	}
	result.Body = strings.TrimSpace(body.String())

	return result, scanner.Err()
}

// ParseMarkdownFile parses a markdown file from disk.
func ParseMarkdownFile(path string) (ParsedMarkdown, error) {
	f, err := os.Open(path)
	if err != nil {
		return ParsedMarkdown{}, err
	}
	defer f.Close()
	return ParseMarkdown(f)
}

// AgentMode distinguishes persistent from ephemeral agents.
// Coordinator is a superset of persistent — it adds manager tools
// and write access to agents/ and skills/ directories.
type AgentMode string

const (
	ModePersistent  AgentMode = "persistent"
	ModeEphemeral   AgentMode = "ephemeral"
	ModeCoordinator AgentMode = "coordinator"
)

// IsPersistent reports whether the mode has persistent-agent capabilities
// (memory, todos, history, session restoration). Both persistent and
// coordinator modes are persistent.
func (m AgentMode) IsPersistent() bool {
	return m == ModePersistent || m == ModeCoordinator
}

// AgentConfig represents an agent's configuration loaded from markdown.
type AgentConfig struct {
	Name          string
	Description   string
	DeclaredTools []string // from frontmatter "tools" field; nil = no built-in tools (closed by default)
	Prompt        string   // the markdown body — the agent's operating instructions
	Skills        []SkillConfig
}

// SkillConfig represents a skill definition loaded from markdown.
type SkillConfig struct {
	Name          string
	Description   string
	Prompt        string            // the markdown body — instructions for this skill
	Path          string            // absolute path to the skill file (for progressive disclosure)
	License       string            // optional: license identifier (e.g. MIT, Apache-2.0)
	Compatibility string            // optional: system/dependency requirements (max 500 chars)
	Metadata      map[string]string // optional: arbitrary key-value pairs (author, version, etc.)
}

var validSkillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidateSkillName checks that a skill name is kebab-case, max 64 characters.
func ValidateSkillName(name string) error {
	if name == "" {
		return fmt.Errorf("skill name is required")
	}
	if len(name) > 64 {
		return fmt.Errorf("skill name %q exceeds 64 character limit", name)
	}
	if !validSkillName.MatchString(name) {
		return fmt.Errorf("skill name %q must be kebab-case (letters, numbers, hyphens)", name)
	}
	return nil
}

// LoadAgentDir loads an agent configuration from a directory containing
// an agent.md file and an optional skills/ subdirectory.
func LoadAgentDir(dir string) (AgentConfig, error) {
	agentPath := filepath.Join(dir, "agent.md")
	parsed, err := ParseMarkdownFile(agentPath)
	if err != nil {
		return AgentConfig{}, fmt.Errorf("loading agent config: %w", err)
	}

	agent := AgentConfig{
		Name:          parsed.Frontmatter.String("name"),
		Description:   parsed.Frontmatter.String("description"),
		DeclaredTools: parsed.Frontmatter.StringSlice("tools"),
		Prompt:        parsed.Body,
	}

	if agent.Name == "" {
		return AgentConfig{}, fmt.Errorf("agent config at %s missing required 'name' field", agentPath)
	}

	// Load skills
	skills, err := LoadSkills(filepath.Join(dir, "skills"))
	if err != nil {
		return AgentConfig{}, err
	}
	agent.Skills = skills

	return agent, nil
}

// ReloadAgentTexts re-reads the prompt body from agent.md on disk.
// Structural frontmatter (name, model, tools) is parsed but discarded —
// only the body text matters for hot-reload.
func ReloadAgentTexts(dir string) (prompt string, err error) {
	parsed, err := ParseMarkdownFile(filepath.Join(dir, "agent.md"))
	if err != nil {
		return "", fmt.Errorf("reloading agent.md: %w", err)
	}
	return parsed.Body, nil
}

// LoadSkills loads all skill configs from a skills directory.
// Supports both flat files (skill.md) and directories (skill/SKILL.md).
// Returns nil and no error if the directory does not exist.
func LoadSkills(skillsDir string) ([]SkillConfig, error) {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading skills directory: %w", err)
	}

	var skills []SkillConfig
	for _, entry := range entries {
		var skillPath string
		if entry.IsDir() {
			// Directory skill: look for SKILL.md inside
			candidate := filepath.Join(skillsDir, entry.Name(), "SKILL.md")
			if _, err := os.Stat(candidate); err != nil {
				continue // directory without SKILL.md, skip
			}
			skillPath = candidate
		} else if strings.HasSuffix(entry.Name(), ".md") {
			skillPath = filepath.Join(skillsDir, entry.Name())
		} else {
			continue
		}

		skill, err := loadSkillFile(skillPath)
		if err != nil {
			return nil, fmt.Errorf("loading skill %s: %w", entry.Name(), err)
		}

		// For directory skills, validate name matches directory name
		if entry.IsDir() && !strings.EqualFold(skill.Name, entry.Name()) {
			return nil, fmt.Errorf(
				"skill name %q in %s must match directory name %q (case-insensitive)",
				skill.Name, skillPath, entry.Name())
		}

		skills = append(skills, skill)
	}

	return skills, nil
}

// MergeSkills combines agent-specific and shared skills.
// Agent skills take precedence over shared skills with the same name.
func MergeSkills(agentSkills, sharedSkills []SkillConfig) []SkillConfig {
	seen := make(map[string]bool, len(agentSkills))
	for _, s := range agentSkills {
		seen[s.Name] = true
	}
	merged := make([]SkillConfig, len(agentSkills))
	copy(merged, agentSkills)
	for _, s := range sharedSkills {
		if !seen[s.Name] {
			merged = append(merged, s)
		}
	}
	return merged
}

// ReadOptionalFile reads a file and returns its trimmed content.
// Returns empty string and nil error if the file does not exist.
func ReadOptionalFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func loadSkillFile(path string) (SkillConfig, error) {
	parsed, err := ParseMarkdownFile(path)
	if err != nil {
		return SkillConfig{}, err
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return SkillConfig{}, fmt.Errorf("resolving skill path: %w", err)
	}

	skill := SkillConfig{
		Name:          parsed.Frontmatter.String("name"),
		Description:   parsed.Frontmatter.String("description"),
		Prompt:        parsed.Body,
		Path:          absPath,
		License:       parsed.Frontmatter.String("license"),
		Compatibility: parsed.Frontmatter.String("compatibility"),
		Metadata:      parsed.Frontmatter.StringMap("metadata"),
	}

	if err := ValidateSkillName(skill.Name); err != nil {
		return SkillConfig{}, fmt.Errorf("skill at %s: %w", path, err)
	}

	if skill.Description == "" {
		return SkillConfig{}, fmt.Errorf("skill %q at %s missing required 'description' field", skill.Name, path)
	}
	if len(skill.Description) > 1024 {
		return SkillConfig{}, fmt.Errorf("skill %q description exceeds 1024 character limit", skill.Name)
	}
	if len(skill.Compatibility) > 500 {
		return SkillConfig{}, fmt.Errorf("skill %q compatibility exceeds 500 character limit", skill.Name)
	}

	return skill, nil
}
