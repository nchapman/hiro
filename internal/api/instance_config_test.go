package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetInstanceConfig_NotFound(t *testing.T) {
	srv, _, _ := newInstanceTestServer(t)

	req := httptest.NewRequest("GET", "/api/instances/nonexistent/config", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rec.Code)
	}
}

func TestGetInstanceConfig_NoManager(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/instances/test/config", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", rec.Code)
	}
}

func TestGetInstanceConfig_ReturnsConfig(t *testing.T) {
	srv, mgr, agentName := newInstanceTestServer(t)

	id, err := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/instances/"+id+"/config", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp instanceConfigResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Should return empty arrays, not null.
	if resp.AllowedTools == nil {
		t.Error("allowed_tools should be empty array, got nil")
	}
	if resp.DisallowedTools == nil {
		t.Error("disallowed_tools should be empty array, got nil")
	}
}

func TestPutInstanceConfig_NoManager(t *testing.T) {
	srv := newTestServer()

	body := `{"model": "test"}`
	req := httptest.NewRequest("PUT", "/api/instances/test/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", rec.Code)
	}
}

func TestPutInstanceConfig_NotFound(t *testing.T) {
	srv, _, _ := newInstanceTestServer(t)

	body := `{"model": "test"}`
	req := httptest.NewRequest("PUT", "/api/instances/nonexistent/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rec.Code)
	}
}

func TestPutInstanceConfig_InvalidJSON(t *testing.T) {
	srv, mgr, agentName := newInstanceTestServer(t)

	id, _ := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")

	req := httptest.NewRequest("PUT", "/api/instances/"+id+"/config", bytes.NewBufferString("{bad"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
}

func TestPutInstanceConfig_StoppedInstance(t *testing.T) {
	srv, mgr, agentName := newInstanceTestServer(t)

	// Create and stop an instance (needs a parent so it can be stopped).
	parentID, err := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("CreateInstance parent: %v", err)
	}
	childID, err := mgr.CreateInstance(context.Background(), agentName, parentID, "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("CreateInstance child: %v", err)
	}
	if _, err := mgr.StopInstance(childID); err != nil {
		t.Fatalf("StopInstance: %v", err)
	}

	// Update config on the stopped instance.
	body := `{"model": "anthropic/claude-sonnet-4-20250514", "reasoning_effort": "high"}`
	req := httptest.NewRequest("PUT", "/api/instances/"+childID+"/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	// Verify config was persisted.
	getReq := httptest.NewRequest("GET", "/api/instances/"+childID+"/config", nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)

	var resp instanceConfigResponse
	json.NewDecoder(getRec.Body).Decode(&resp)
	if resp.Model != "anthropic/claude-sonnet-4-20250514" {
		t.Errorf("model=%q, want anthropic/claude-sonnet-4-20250514", resp.Model)
	}
	if resp.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort=%q, want high", resp.ReasoningEffort)
	}
}

func TestPutInstanceConfig_StoppedToolRules(t *testing.T) {
	srv, mgr, agentName := newInstanceTestServer(t)

	parentID, _ := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")
	childID, _ := mgr.CreateInstance(context.Background(), agentName, parentID, "persistent", "", "", "", "")
	mgr.StopInstance(childID)

	// Set valid tool rules on the stopped instance.
	body := `{"allowed_tools": ["Bash", "Read", "Bash(curl *)"], "disallowed_tools": ["Bash(rm *)"]}`
	req := httptest.NewRequest("PUT", "/api/instances/"+childID+"/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	// Verify tool rules were persisted.
	getReq := httptest.NewRequest("GET", "/api/instances/"+childID+"/config", nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)

	var resp instanceConfigResponse
	json.NewDecoder(getRec.Body).Decode(&resp)
	if len(resp.AllowedTools) != 3 {
		t.Errorf("allowed_tools=%v, want 3 items", resp.AllowedTools)
	}
	if len(resp.DisallowedTools) != 1 || resp.DisallowedTools[0] != "Bash(rm *)" {
		t.Errorf("disallowed_tools=%v, want [Bash(rm *)]", resp.DisallowedTools)
	}
}

func TestPutInstanceConfig_InvalidToolRules(t *testing.T) {
	srv, mgr, agentName := newInstanceTestServer(t)

	parentID, _ := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")
	childID, _ := mgr.CreateInstance(context.Background(), agentName, parentID, "persistent", "", "", "", "")
	mgr.StopInstance(childID)

	// Invalid tool rule syntax (unclosed paren).
	body := `{"allowed_tools": ["Bash("]}`
	req := httptest.NewRequest("PUT", "/api/instances/"+childID+"/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
