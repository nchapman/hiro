package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// encryptPath encrypts a relative file path into an opaque hex token using AES-GCM.
func (s *Server) encryptPath(relPath string) (string, error) {
	secret, err := s.shareSecret()
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(secret)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(relPath), nil)
	return base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// decryptToken decrypts a hex token back into a relative file path.
func (s *Server) decryptToken(token string) (string, error) {
	secret, err := s.shareSecret()
	if err != nil {
		return "", err
	}

	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(secret)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", err
	}

	plaintext, err := gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// shareSecret returns the 32-byte signing secret from the control plane,
// reused for share token encryption.
func (s *Server) shareSecret() ([]byte, error) {
	signer, err := s.cp.TokenSigner()
	if err != nil {
		return nil, err
	}
	return signer.Secret(), nil
}

// handleShareCreate creates a share token for a file path.
// POST /api/files/share  { "path": "workspace/foo.md" }
// Returns { "token": "abc123..." }
func (s *Server) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	// Block sharing config.yaml (contains secrets).
	if body.Path == "config.yaml" {
		http.Error(w, "config.yaml cannot be shared", http.StatusForbidden)
		return
	}

	// Verify the file exists and is within root.
	absPath, err := resolveFilesPath(s.rootDir, body.Path)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		s.logger.Error("share stat failed", "path", absPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.Error(w, "cannot share a directory", http.StatusBadRequest)
		return
	}

	token, err := s.encryptPath(body.Path)
	if err != nil {
		s.logger.Error("share token creation failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

// resolveShareToken decrypts a token and resolves the path.
// Returns the relative path and absolute path, or writes an error response.
func (s *Server) resolveShareToken(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	token := r.PathValue("token")
	relPath, err := s.decryptToken(token)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return "", "", false
	}

	absPath, err := resolveFilesPath(s.rootDir, relPath)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return "", "", false
	}

	return relPath, absPath, true
}

// handleSharedFileInfo returns metadata + text content for a shared file.
// GET /api/shared/{token}
// Returns { "name": "foo.md", "size": 1234, "content": "..." }
// For binary files, content is omitted.
func (s *Server) handleSharedFileInfo(w http.ResponseWriter, r *http.Request) {
	relPath, absPath, ok := s.resolveShareToken(w, r)
	if !ok {
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file no longer exists", http.StatusGone)
			return
		}
		s.logger.Error("shared file stat failed", "path", absPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := map[string]any{
		"name": filepath.Base(relPath),
		"size": info.Size(),
	}

	// For non-huge text files, include content inline.
	if info.Size() <= maxFileReadSize {
		data, err := os.ReadFile(absPath)
		if err == nil && !isBinaryData(data) {
			resp["content"] = string(data)
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleSharedFileRaw serves the raw file content (for binary preview, images, etc).
// GET /api/shared/{token}/raw
func (s *Server) handleSharedFileRaw(w http.ResponseWriter, r *http.Request) {
	_, absPath, ok := s.resolveShareToken(w, r)
	if !ok {
		return
	}

	f, err := os.Open(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file no longer exists", http.StatusGone)
			return
		}
		s.logger.Error("shared file open failed", "path", absPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Force download for types that could contain active content (XSS via SVG/HTML).
	ext := strings.ToLower(filepath.Ext(filepath.Base(absPath)))
	if ext == ".svg" || ext == ".html" || ext == ".htm" {
		w.Header().Set("Content-Disposition", "attachment")
		w.Header().Set("Content-Type", "application/octet-stream")
	}

	http.ServeContent(w, r, filepath.Base(absPath), info.ModTime(), f)
}

// isBinaryData checks if data contains null bytes (indicating binary content).
func isBinaryData(data []byte) bool {
	check := data
	if len(check) > 8192 {
		check = check[:8192]
	}
	for _, b := range check {
		if b == 0 {
			return true
		}
	}
	return false
}
