package tools

import (
	"path/filepath"
	"testing"
)

func TestResolvePath(t *testing.T) {
	wd := "/home/agent/workspace"
	tests := []struct {
		name       string
		workingDir string
		path       string
		want       string
	}{
		{
			name:       "relative path",
			workingDir: wd,
			path:       "agents/foo/agent.md",
			want:       "/home/agent/workspace/agents/foo/agent.md",
		},
		{
			name:       "absolute path unchanged",
			workingDir: wd,
			path:       "/etc/hosts",
			want:       "/etc/hosts",
		},
		{
			name:       "dot-dot in relative path is cleaned",
			workingDir: wd,
			path:       "agents/../agents/foo.md",
			want:       "/home/agent/workspace/agents/foo.md",
		},
		{
			name:       "dot path resolves to workingDir",
			workingDir: wd,
			path:       ".",
			want:       "/home/agent/workspace",
		},
		{
			name:       "bare filename",
			workingDir: wd,
			path:       "file.txt",
			want:       "/home/agent/workspace/file.txt",
		},
		{
			name:       "nested relative path",
			workingDir: wd,
			path:       "a/b/c/d.txt",
			want:       "/home/agent/workspace/a/b/c/d.txt",
		},
		{
			name:       "empty workingDir falls back to relative",
			workingDir: "",
			path:       "file.txt",
			want:       "file.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvePath(tt.workingDir, tt.path)
			// Clean both for comparison since Join may clean
			if filepath.Clean(got) != filepath.Clean(tt.want) {
				t.Errorf("resolvePath(%q, %q) = %q, want %q", tt.workingDir, tt.path, got, tt.want)
			}
		})
	}
}
