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

func TestE2E_ListInstances(t *testing.T) {
	instances := listInstances(t)
	if len(instances) == 0 {
		t.Fatal("expected at least one instance (coordinator)")
	}

	found := false
	for _, inst := range instances {
		if inst.Name == "coordinator" {
			found = true
			if inst.Mode != "coordinator" {
				t.Errorf("coordinator mode: expected coordinator, got %q", inst.Mode)
			}
			break
		}
	}
	if !found {
		t.Error("coordinator not found in instance list")
	}
}
