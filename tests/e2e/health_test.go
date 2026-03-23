//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestE2E_Health(t *testing.T) {
	resp, err := http.Get(baseURL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", result["status"])
	}
}

func TestE2E_ListAgents(t *testing.T) {
	agents := listAgents(t)
	if len(agents) == 0 {
		t.Fatal("expected at least one agent (coordinator)")
	}

	found := false
	for _, a := range agents {
		if a.Name == "coordinator" {
			found = true
			if a.Mode != "persistent" {
				t.Errorf("coordinator mode: expected persistent, got %q", a.Mode)
			}
			break
		}
	}
	if !found {
		t.Error("coordinator not found in agent list")
	}
}
