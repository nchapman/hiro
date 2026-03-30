package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nchapman/hivebot/internal/controlplane"
)

func TestShareEncryptDecryptRoundtrip(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	// encryptPath and decryptToken should be inverse operations.
	original := "workspace/test-file.md"
	token, err := srv.encryptPath(original)
	if err != nil {
		t.Fatalf("encryptPath: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	decrypted, err := srv.decryptToken(token)
	if err != nil {
		t.Fatalf("decryptToken: %v", err)
	}
	if decrypted != original {
		t.Errorf("roundtrip failed: got %q, want %q", decrypted, original)
	}
}

func TestShareEncrypt_DifferentTokensForSamePath(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	// Due to random nonce, same path should produce different tokens.
	tok1, _ := srv.encryptPath("test.md")
	tok2, _ := srv.encryptPath("test.md")
	if tok1 == tok2 {
		t.Error("expected different tokens for same path (random nonce)")
	}

	// But both should decrypt to the same path.
	dec1, _ := srv.decryptToken(tok1)
	dec2, _ := srv.decryptToken(tok2)
	if dec1 != "test.md" || dec2 != "test.md" {
		t.Errorf("both should decrypt to test.md, got %q and %q", dec1, dec2)
	}
}

func TestShareDecrypt_InvalidToken(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	_, err := srv.decryptToken("not-valid-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64 token")
	}

	// Valid base64 but long enough to pass nonce check, wrong ciphertext.
	_, err = srv.decryptToken("YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo")
	if err == nil {
		t.Error("expected error for invalid ciphertext")
	}
}

func TestHandleShareCreate(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	// Create a file to share.
	root := t.TempDir()
	srv.rootDir = root
	os.MkdirAll(filepath.Join(root, "workspace"), 0755)
	os.WriteFile(filepath.Join(root, "workspace", "hello.txt"), []byte("content"), 0644)

	body, _ := json.Marshal(map[string]string{"path": "workspace/hello.txt"})
	req := authedRequest(t, srv, "POST", "/api/files/share", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["token"] == "" {
		t.Error("expected non-empty token in response")
	}
}

func TestHandleShareCreate_ConfigYamlBlocked(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	root := t.TempDir()
	srv.rootDir = root
	os.WriteFile(filepath.Join(root, "config.yaml"), []byte("secrets: {}"), 0600)

	body, _ := json.Marshal(map[string]string{"path": "config.yaml"})
	req := authedRequest(t, srv, "POST", "/api/files/share", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Error("expected config.yaml sharing to be blocked")
	}
}

func TestHandleSharedFile(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	root := t.TempDir()
	srv.rootDir = root
	os.MkdirAll(filepath.Join(root, "workspace"), 0755)
	os.WriteFile(filepath.Join(root, "workspace", "shared.txt"), []byte("hello world"), 0644)

	// Create a share token.
	token, err := srv.encryptPath("workspace/shared.txt")
	if err != nil {
		t.Fatal(err)
	}

	// Access the shared file (no auth required).
	req := httptest.NewRequest("GET", "/api/shared/"+token, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleSharedFile_InvalidToken(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	srv.rootDir = t.TempDir()

	// Use a token long enough to pass nonce check but still invalid.
	req := httptest.NewRequest("GET", "/api/shared/YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Error("expected error for invalid token")
	}
}

func TestHandleDeleteProvider_PreventLastProvider(t *testing.T) {
	srv, cp := newAuthTestServer(t)

	// Set up exactly one provider.
	cp.SetProvider("anthropic", controlplane.ProviderConfig{APIKey: "sk-test-1234567890"})
	cp.Save()

	req := authedRequest(t, srv, "DELETE", "/api/settings/providers/anthropic", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 when deleting only provider, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleDeleteProvider_AllowsWhenMultiple(t *testing.T) {
	srv, cp := newAuthTestServer(t)

	cp.SetProvider("anthropic", controlplane.ProviderConfig{APIKey: "sk-ant-test"})
	cp.SetProvider("openrouter", controlplane.ProviderConfig{APIKey: "sk-or-test"})
	cp.Save()

	req := authedRequest(t, srv, "DELETE", "/api/settings/providers/anthropic", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 when deleting non-last provider, got %d: %s", rec.Code, rec.Body.String())
	}
}

