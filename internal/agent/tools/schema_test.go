package tools

import "testing"

func TestRemoteToolInfos_ReturnsAllTools(t *testing.T) {
	infos := RemoteToolInfos("/tmp/test")
	if len(infos) != len(RemoteToolNames) {
		t.Fatalf("RemoteToolInfos returned %d tools, want %d", len(infos), len(RemoteToolNames))
	}

	names := make(map[string]bool)
	for _, info := range infos {
		names[info.Name] = true
	}

	for name := range RemoteToolNames {
		if !names[name] {
			t.Errorf("missing tool %q in RemoteToolInfos output", name)
		}
	}
}

func TestRemoteToolInfos_HasDescriptions(t *testing.T) {
	infos := RemoteToolInfos("/workspace")
	for _, info := range infos {
		if info.Name == "" {
			t.Error("tool info has empty name")
		}
		if info.Description == "" {
			t.Errorf("tool %q has empty description", info.Name)
		}
	}
}
