package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/crypto/hkdf"
)

// binaryCheckSize is the number of bytes to scan when detecting binary content.
const binaryCheckSize = 8192

// shareKeyLen is the AES-256 key length in bytes for share token encryption.
const shareKeyLen = 32

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
		return "", fmt.Errorf("share token too short")
	}

	plaintext, err := gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// shareSecret derives a 32-byte AES key from the session signing secret
// using HKDF, so share tokens use a separate key from session HMAC.
func (s *Server) shareSecret() ([]byte, error) {
	signer, err := s.cp.TokenSigner()
	if err != nil {
		return nil, err
	}
	// Nil salt is safe: the IKM (session signing secret) is already a 32-byte uniform random key.
	r := hkdf.New(sha256.New, signer.Secret(), nil, []byte("hiro-share-token-v1"))
	key := make([]byte, shareKeyLen)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, err
	}
	return key, nil
}

// handleShareCreate creates a share token for a file path.
// POST /api/files/share  { "path": "workspace/foo.md" }
// Returns { "token": "abc123..." }
func (s *Server) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodySize)
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	// Verify the file exists and is within root.
	absPath, err := resolveFilesPath(s.rootDir, body.Path)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	// Block sharing anything in protected directories (config, etc.) after
	// path resolution to prevent traversal bypasses like ./config/config.yaml.
	realRoot, err := filepath.EvalSymlinks(s.rootDir)
	if err != nil {
		s.logger.Error("root resolution failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if isProtectedPath(realRoot, absPath) {
		http.Error(w, "this path cannot be shared", http.StatusForbidden)
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

	// Block access to protected paths in case a token was minted before
	// the creation-time check was added.
	if realRoot, err := filepath.EvalSymlinks(s.rootDir); err == nil {
		if isProtectedPath(realRoot, absPath) {
			http.Error(w, "not found", http.StatusNotFound)
			return "", "", false
		}
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

	info, err := os.Stat(absPath) //nolint:gosec // absPath validated by resolveShareToken
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
		data, err := os.ReadFile(absPath) //nolint:gosec // absPath validated by resolveShareToken
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

	f, err := os.Open(absPath) //nolint:gosec // absPath validated by resolveShareToken
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

	// Prevent any active content from executing in shared file responses.
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")

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
	if len(check) > binaryCheckSize {
		check = check[:binaryCheckSize]
	}
	return slices.Contains(check, 0)
}
