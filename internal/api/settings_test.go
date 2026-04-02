package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetSettings(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	req := authedRequest(t, srv, "GET", "/api/settings", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	// Should have default_model key (unified provider/model format).
	if _, ok := resp["default_model"]; !ok {
		t.Error("missing default_model in response")
	}
}

func TestPutProvider(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	body, _ := json.Marshal(map[string]string{
		"api_key": "sk-test-1234567890",
	})
	req := authedRequest(t, srv, "PUT", "/api/settings/providers/anthropic", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Verify it appears in the list.
	req2 := authedRequest(t, srv, "GET", "/api/settings/providers", nil)
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("list providers status=%d body=%s", rec2.Code, rec2.Body.String())
	}

	var providers map[string]any
	json.NewDecoder(rec2.Body).Decode(&providers)
	if _, ok := providers["anthropic"]; !ok {
		t.Errorf("anthropic provider not found in list: %v", providers)
	}
}

func TestDeleteProvider_PreventsSingleton(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	// Add a provider first.
	body, _ := json.Marshal(map[string]string{"api_key": "sk-test"})
	req := authedRequest(t, srv, "PUT", "/api/settings/providers/anthropic", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT provider: status=%d", rec.Code)
	}

	// Try to delete the only provider.
	req2 := authedRequest(t, srv, "DELETE", "/api/settings/providers/anthropic", nil)
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 when deleting only provider", rec2.Code)
	}
}

func TestUpdateSettings(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	// Add a provider so we can set it as default.
	body, _ := json.Marshal(map[string]string{"api_key": "sk-test"})
	req := authedRequest(t, srv, "PUT", "/api/settings/providers/anthropic", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// Update default model.
	body2, _ := json.Marshal(map[string]string{
		"default_model":    "claude-sonnet-4-20250514",
		"default_provider": "anthropic",
	})
	req2 := authedRequest(t, srv, "PUT", "/api/settings", body2)
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec2.Code, rec2.Body.String())
	}

	// Verify via GET.
	req3 := authedRequest(t, srv, "GET", "/api/settings", nil)
	rec3 := httptest.NewRecorder()
	srv.ServeHTTP(rec3, req3)

	var settings map[string]any
	json.NewDecoder(rec3.Body).Decode(&settings)
	if settings["default_model"] != "claude-sonnet-4-20250514" {
		t.Errorf("default_model=%v, want claude-sonnet-4-20250514", settings["default_model"])
	}
}

func TestSettings_RequiresAuth(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	req := httptest.NewRequest("GET", "/api/settings", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 for unauthenticated settings", rec.Code)
	}
}
