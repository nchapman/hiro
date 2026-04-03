package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPersonaFile_NotExists(t *testing.T) {
	dir := t.TempDir()
	pd, err := ReadPersonaFile(dir)
	if err != nil {
		t.Fatalf("ReadPersonaFile: %v", err)
	}
	if pd.Name != "" || pd.Description != "" || pd.Body != "" {
		t.Errorf("expected empty PersonaData, got %+v", pd)
	}
}

func TestWriteAndReadPersonaFile_BodyOnly(t *testing.T) {
	dir := t.TempDir()
	want := "Friendly and precise. Prefers concise answers."

	if err := WritePersonaFile(dir, "", "", want); err != nil {
		t.Fatalf("WritePersonaFile: %v", err)
	}

	pd, err := ReadPersonaFile(dir)
	if err != nil {
		t.Fatalf("ReadPersonaFile: %v", err)
	}
	if pd.Body != want {
		t.Errorf("body = %q, want %q", pd.Body, want)
	}
	if pd.Name != "" {
		t.Errorf("name = %q, want empty", pd.Name)
	}
}

func TestWriteAndReadPersonaFile_WithFrontmatter(t *testing.T) {
	dir := t.TempDir()

	if err := WritePersonaFile(dir, "Backend Lead", "Owns the API rewrite", "Be thorough."); err != nil {
		t.Fatalf("WritePersonaFile: %v", err)
	}

	pd, err := ReadPersonaFile(dir)
	if err != nil {
		t.Fatalf("ReadPersonaFile: %v", err)
	}
	if pd.Name != "Backend Lead" {
		t.Errorf("name = %q, want %q", pd.Name, "Backend Lead")
	}
	if pd.Description != "Owns the API rewrite" {
		t.Errorf("description = %q, want %q", pd.Description, "Owns the API rewrite")
	}
	if pd.Body != "Be thorough." {
		t.Errorf("body = %q, want %q", pd.Body, "Be thorough.")
	}
}

func TestWritePersonaFile_NameOnly(t *testing.T) {
	dir := t.TempDir()

	if err := WritePersonaFile(dir, "Alice", "", ""); err != nil {
		t.Fatalf("WritePersonaFile: %v", err)
	}

	pd, err := ReadPersonaFile(dir)
	if err != nil {
		t.Fatalf("ReadPersonaFile: %v", err)
	}
	if pd.Name != "Alice" {
		t.Errorf("name = %q, want %q", pd.Name, "Alice")
	}
	if pd.Body != "" {
		t.Errorf("body = %q, want empty", pd.Body)
	}
}

func TestReadPersonaFile_AgentEditedFrontmatter(t *testing.T) {
	// Simulate an agent editing persona.md with frontmatter directly via file tools.
	dir := t.TempDir()
	content := "---\nname: Custom Name\ndescription: Custom desc\n---\n\nPersona instructions here."
	if err := os.WriteFile(filepath.Join(dir, "persona.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	pd, err := ReadPersonaFile(dir)
	if err != nil {
		t.Fatalf("ReadPersonaFile: %v", err)
	}
	if pd.Name != "Custom Name" {
		t.Errorf("name = %q, want %q", pd.Name, "Custom Name")
	}
	if pd.Description != "Custom desc" {
		t.Errorf("description = %q, want %q", pd.Description, "Custom desc")
	}
	if pd.Body != "Persona instructions here." {
		t.Errorf("body = %q, want %q", pd.Body, "Persona instructions here.")
	}
}

func TestReadPersonaFile_PlainMarkdown(t *testing.T) {
	// Backward compat: persona.md with no frontmatter is treated as body-only.
	dir := t.TempDir()
	content := "Just some persona text, no frontmatter."
	if err := os.WriteFile(filepath.Join(dir, "persona.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	pd, err := ReadPersonaFile(dir)
	if err != nil {
		t.Fatalf("ReadPersonaFile: %v", err)
	}
	if pd.Name != "" {
		t.Errorf("name = %q, want empty", pd.Name)
	}
	if pd.Body != content {
		t.Errorf("body = %q, want %q", pd.Body, content)
	}
}

func TestWritePersonaFile_Overwrites(t *testing.T) {
	dir := t.TempDir()

	if err := WritePersonaFile(dir, "First", "", "first version"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WritePersonaFile(dir, "Second", "new desc", "second version"); err != nil {
		t.Fatalf("second write: %v", err)
	}

	pd, err := ReadPersonaFile(dir)
	if err != nil {
		t.Fatalf("ReadPersonaFile: %v", err)
	}
	if pd.Name != "Second" {
		t.Errorf("name = %q, want %q", pd.Name, "Second")
	}
	if pd.Body != "second version" {
		t.Errorf("body = %q, want %q", pd.Body, "second version")
	}
}

func TestWritePersonaFile_SpecialYAMLValues(t *testing.T) {
	// Values that are YAML-special must round-trip correctly through yaml.Marshal.
	cases := []struct {
		name string
		desc string
	}{
		{"true", "false"},
		{"null", "~"},
		{"name: injected", "desc: injected"},
		{"line1\nline2", "has\nnewlines"},
		{`"quoted"`, `'single quoted'`},
		{"colons: everywhere", "hashes # here"},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		if err := WritePersonaFile(dir, tc.name, tc.desc, "body"); err != nil {
			t.Fatalf("WritePersonaFile(%q, %q): %v", tc.name, tc.desc, err)
		}
		pd, err := ReadPersonaFile(dir)
		if err != nil {
			t.Fatalf("ReadPersonaFile: %v", err)
		}
		if pd.Name != tc.name {
			t.Errorf("name = %q, want %q", pd.Name, tc.name)
		}
		if pd.Description != tc.desc {
			t.Errorf("description = %q, want %q", pd.Description, tc.desc)
		}
		if pd.Body != "body" {
			t.Errorf("body = %q, want %q", pd.Body, "body")
		}
	}
}

func TestWritePersonaFile_Permissions(t *testing.T) {
	dir := t.TempDir()
	WritePersonaFile(dir, "", "", "agent persona")

	info, err := os.Stat(filepath.Join(dir, "persona.md"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("expected 0o600 permissions, got %o", perm)
	}
}
