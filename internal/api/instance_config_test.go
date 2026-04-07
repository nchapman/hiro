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

func TestMaskToken(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"", ""},
		{"abc", "••••"},                                      // short → fully masked
		{"abcdefgh", "••••"},                                 // 8 chars → fully masked
		{"abcdefghi", "••••fghi"},                            // 9 chars → show last 4
		{"12345678901234567890", "••••7890"},                 // 20 chars → show last 4
		{"123456789012345678901", "1234••••8901"},            // 21 chars → show first 4 + last 4
		{"1234567890:AAHfoo_bar-bazQux0WYk", "1234••••0WYk"}, // long token
	}
	for _, tc := range tests {
		if got := maskToken(tc.input); got != tc.want {
			t.Errorf("maskToken(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestPutInstanceConfig_Persona(t *testing.T) {
	srv, mgr, agentName := newInstanceTestServer(t)

	parentID, _ := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")
	childID, _ := mgr.CreateInstance(context.Background(), agentName, parentID, "persistent", "", "", "", "")
	mgr.StopInstance(childID)

	// Set persona fields.
	body := `{"persona_name": "Alice", "persona_description": "Research assistant", "persona_body": "Be thorough."}`
	req := httptest.NewRequest("PUT", "/api/instances/"+childID+"/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT status=%d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	// Verify persona was persisted via GET.
	getReq := httptest.NewRequest("GET", "/api/instances/"+childID+"/config", nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)

	var resp instanceConfigResponse
	json.NewDecoder(getRec.Body).Decode(&resp)
	if resp.PersonaName != "Alice" {
		t.Errorf("persona_name=%q, want Alice", resp.PersonaName)
	}
	if resp.PersonaDesc != "Research assistant" {
		t.Errorf("persona_description=%q, want Research assistant", resp.PersonaDesc)
	}
	if resp.PersonaBody != "Be thorough." {
		t.Errorf("persona_body=%q, want 'Be thorough.'", resp.PersonaBody)
	}

	// Partial update — only change name, body should be preserved.
	body2 := `{"persona_name": "Bob"}`
	req2 := httptest.NewRequest("PUT", "/api/instances/"+childID+"/config", bytes.NewBufferString(body2))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNoContent {
		t.Fatalf("PUT2 status=%d, want 204", rec2.Code)
	}

	getRec2 := httptest.NewRecorder()
	srv.ServeHTTP(getRec2, httptest.NewRequest("GET", "/api/instances/"+childID+"/config", nil))
	var resp2 instanceConfigResponse
	json.NewDecoder(getRec2.Body).Decode(&resp2)
	if resp2.PersonaName != "Bob" {
		t.Errorf("persona_name=%q, want Bob", resp2.PersonaName)
	}
	if resp2.PersonaBody != "Be thorough." {
		t.Errorf("persona_body=%q, want preserved 'Be thorough.'", resp2.PersonaBody)
	}
}

func TestPutInstanceConfig_Memory(t *testing.T) {
	srv, mgr, agentName := newInstanceTestServer(t)

	parentID, _ := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")
	childID, _ := mgr.CreateInstance(context.Background(), agentName, parentID, "persistent", "", "", "", "")
	mgr.StopInstance(childID)

	body := `{"memory": "User prefers Go over Python."}`
	req := httptest.NewRequest("PUT", "/api/instances/"+childID+"/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT status=%d, want 204", rec.Code)
	}

	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, httptest.NewRequest("GET", "/api/instances/"+childID+"/config", nil))
	var resp instanceConfigResponse
	json.NewDecoder(getRec.Body).Decode(&resp)
	if resp.Memory != "User prefers Go over Python." {
		t.Errorf("memory=%q, want 'User prefers Go over Python.'", resp.Memory)
	}
}

func TestPutInstanceConfig_TelegramChannel(t *testing.T) {
	srv, mgr, agentName := newInstanceTestServer(t)

	parentID, _ := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")
	childID, _ := mgr.CreateInstance(context.Background(), agentName, parentID, "persistent", "", "", "", "")
	mgr.StopInstance(childID)

	// Add Telegram channel.
	body := `{"telegram": {"bot_token": "123456:AAHfoobar"}}`
	req := httptest.NewRequest("PUT", "/api/instances/"+childID+"/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT status=%d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	// GET should return masked token.
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, httptest.NewRequest("GET", "/api/instances/"+childID+"/config", nil))
	var resp instanceConfigResponse
	json.NewDecoder(getRec.Body).Decode(&resp)
	if resp.Telegram == nil {
		t.Fatal("telegram should be configured")
	}
	if resp.Telegram.BotToken != "••••obar" {
		t.Errorf("telegram.bot_token=%q, want masked ••••obar", resp.Telegram.BotToken)
	}

	// Remove Telegram channel (empty bot_token).
	body2 := `{"telegram": {"bot_token": ""}}`
	req2 := httptest.NewRequest("PUT", "/api/instances/"+childID+"/config", bytes.NewBufferString(body2))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNoContent {
		t.Fatalf("PUT remove status=%d, want 204", rec2.Code)
	}

	getRec2 := httptest.NewRecorder()
	srv.ServeHTTP(getRec2, httptest.NewRequest("GET", "/api/instances/"+childID+"/config", nil))
	var resp2 instanceConfigResponse
	json.NewDecoder(getRec2.Body).Decode(&resp2)
	if resp2.Telegram != nil {
		t.Errorf("telegram should be nil after removal, got %+v", resp2.Telegram)
	}
}

func TestPutInstanceConfig_SlackChannel(t *testing.T) {
	srv, mgr, agentName := newInstanceTestServer(t)

	parentID, _ := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")
	childID, _ := mgr.CreateInstance(context.Background(), agentName, parentID, "persistent", "", "", "", "")
	mgr.StopInstance(childID)

	// Add Slack channel.
	body := `{"slack": {"bot_token": "xoxb-test-token", "signing_secret": "secret123"}}`
	req := httptest.NewRequest("PUT", "/api/instances/"+childID+"/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT status=%d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, httptest.NewRequest("GET", "/api/instances/"+childID+"/config", nil))
	var resp instanceConfigResponse
	json.NewDecoder(getRec.Body).Decode(&resp)
	if resp.Slack == nil {
		t.Fatal("slack should be configured")
	}
	if resp.Slack.BotToken != "••••oken" {
		t.Errorf("slack.bot_token=%q, want masked ••••oken", resp.Slack.BotToken)
	}
	if resp.Slack.SigningSecret != "••••t123" {
		t.Errorf("slack.signing_secret=%q, want masked ••••t123", resp.Slack.SigningSecret)
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
