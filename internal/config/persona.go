package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nchapman/hiro/internal/platform/fsperm"
	"gopkg.in/yaml.v3"
)

const personaFileName = "persona.md"

// personaFrontmatter is the YAML structure for persona.md frontmatter.
type personaFrontmatter struct {
	Name        string `yaml:"name,omitempty"`
	Description string `yaml:"description,omitempty"`
}

// PersonaData holds the parsed contents of a persona.md file.
// Name and Description are optional frontmatter fields that override
// the agent definition defaults for display purposes.
type PersonaData struct {
	Name        string // display name (from frontmatter)
	Description string // display description (from frontmatter)
	Body        string // persona instructions (goes into system prompt)
}

// ForPrompt returns the persona content formatted for inclusion in the system prompt.
// Includes name and description (if set) followed by the body.
func (pd PersonaData) ForPrompt() string {
	var sb strings.Builder
	if pd.Name != "" {
		sb.WriteString("Name: ")
		sb.WriteString(pd.Name)
		sb.WriteString("\n")
	}
	if pd.Description != "" {
		sb.WriteString("Description: ")
		sb.WriteString(pd.Description)
		sb.WriteString("\n")
	}
	if sb.Len() > 0 && pd.Body != "" {
		sb.WriteString("\n")
	}
	sb.WriteString(pd.Body)
	return strings.TrimSpace(sb.String())
}

// ReadPersonaFile reads and parses the persona.md file from the given instance
// directory. Returns zero PersonaData and nil error if the file does not exist.
func ReadPersonaFile(instanceDir string) (PersonaData, error) {
	path := filepath.Join(instanceDir, personaFileName)
	data, err := os.ReadFile(path) //nolint:gosec // path is constructed from instance dir, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return PersonaData{}, nil
		}
		return PersonaData{}, err
	}

	content := strings.TrimSpace(string(data))
	if content == "" {
		return PersonaData{}, nil
	}

	parsed, err := ParseMarkdown(strings.NewReader(content))
	if err != nil {
		// If frontmatter parsing fails, treat entire content as body.
		return PersonaData{Body: content}, nil //nolint:nilerr // graceful degradation: unparseable frontmatter is fine
	}

	return PersonaData{
		Name:        parsed.Frontmatter.String("name"),
		Description: parsed.Frontmatter.String("description"),
		Body:        strings.TrimSpace(parsed.Body),
	}, nil
}

// WritePersonaFile writes content to the persona.md file in the given instance
// directory, creating it if it doesn't exist. Uses atomic write (temp+rename)
// so concurrent readers never see partial content.
//
// If name or description are non-empty, they are written as YAML frontmatter.
func WritePersonaFile(instanceDir, name, description, body string) error {
	path := filepath.Join(instanceDir, personaFileName)
	if err := os.MkdirAll(filepath.Dir(path), fsperm.DirPrivate); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	var sb strings.Builder
	if name != "" || description != "" {
		fm := personaFrontmatter{Name: name, Description: description}
		fmBytes, err := yaml.Marshal(fm)
		if err != nil {
			return fmt.Errorf("marshaling persona frontmatter: %w", err)
		}
		sb.WriteString("---\n")
		sb.WriteString(string(fmBytes))
		sb.WriteString("---\n")
		if body != "" {
			sb.WriteString("\n")
		}
	}
	sb.WriteString(body)

	return atomicWrite(path, []byte(sb.String()), fsperm.FilePrivate)
}
