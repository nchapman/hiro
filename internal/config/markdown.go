// Package config handles parsing markdown files with YAML frontmatter
// into agent configurations, skill definitions, and other Hive types.
package config

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
type AgentMode string

const (
	ModePersistent AgentMode = "persistent"
	ModeEphemeral  AgentMode = "ephemeral"
)

// AgentConfig represents an agent's configuration loaded from markdown.
type AgentConfig struct {
	Name        string
	Model       string
	Mode        AgentMode // ModePersistent (default) or ModeEphemeral
	Description string
	Prompt      string // the markdown body — the agent's operating instructions
	Soul        string // persona, tone, boundaries (from soul.md)
	Tools       string // tool notes and conventions (from tools.md)
	Skills      []SkillConfig
}

// SkillConfig represents a skill definition loaded from markdown.
type SkillConfig struct {
	Name        string
	Description string
	Prompt      string // the markdown body — instructions for this skill
}

// LoadAgentDir loads an agent configuration from a directory containing
// an agent.md file and an optional skills/ subdirectory.
func LoadAgentDir(dir string) (AgentConfig, error) {
	agentPath := filepath.Join(dir, "agent.md")
	parsed, err := ParseMarkdownFile(agentPath)
	if err != nil {
		return AgentConfig{}, fmt.Errorf("loading agent config: %w", err)
	}

	mode := AgentMode(parsed.Frontmatter.String("mode"))
	if mode == "" {
		mode = ModePersistent
	}
	switch mode {
	case ModePersistent, ModeEphemeral:
		// valid
	default:
		return AgentConfig{}, fmt.Errorf("unknown mode %q in %s (valid: persistent, ephemeral)", mode, agentPath)
	}

	agent := AgentConfig{
		Name:        parsed.Frontmatter.String("name"),
		Model:       parsed.Frontmatter.String("model"),
		Mode:        mode,
		Description: parsed.Frontmatter.String("description"),
		Prompt:      parsed.Body,
	}

	if agent.Name == "" {
		return AgentConfig{}, fmt.Errorf("agent config at %s missing required 'name' field", agentPath)
	}

	// Load optional files
	soul, err := ReadOptionalFile(filepath.Join(dir, "soul.md"))
	if err != nil {
		return AgentConfig{}, fmt.Errorf("reading soul.md: %w", err)
	}
	agent.Soul = soul

	toolsContent, err := ReadOptionalFile(filepath.Join(dir, "tools.md"))
	if err != nil {
		return AgentConfig{}, fmt.Errorf("reading tools.md: %w", err)
	}
	agent.Tools = toolsContent

	// Load skills
	skillsDir := filepath.Join(dir, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return agent, nil // no skills directory is fine
		}
		return AgentConfig{}, fmt.Errorf("reading skills directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		skill, err := loadSkillFile(filepath.Join(skillsDir, entry.Name()))
		if err != nil {
			return AgentConfig{}, fmt.Errorf("loading skill %s: %w", entry.Name(), err)
		}
		agent.Skills = append(agent.Skills, skill)
	}

	return agent, nil
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

	skill := SkillConfig{
		Name:        parsed.Frontmatter.String("name"),
		Description: parsed.Frontmatter.String("description"),
		Prompt:      parsed.Body,
	}

	if skill.Name == "" {
		return SkillConfig{}, fmt.Errorf("skill at %s missing required 'name' field", path)
	}

	return skill, nil
}
