package api

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/nchapman/hiro/internal/controlplane"
	"golang.org/x/crypto/bcrypt"
)

const testPassword = "testpass1"

// testAuthCP creates a ControlPlane with a password set (setup complete) inside dir.
// Returns the CP and a valid session token for use in requests.
func testAuthCP(t *testing.T, dir string) (*controlplane.ControlPlane, string) {
	t.Helper()
	path := filepath.Join(dir, "config", "config.yaml")
	os.MkdirAll(filepath.Dir(path), 0o755)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cp, err := controlplane.Load(path, logger)
	if err != nil {
		t.Fatalf("controlplane.Load: %v", err)
	}

	hash, _ := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	cp.SetPasswordHash(string(hash))
	cp.SetClusterMode("standalone")
	if err := cp.Save(); err != nil {
		t.Fatalf("cp.Save: %v", err)
	}

	signer, err := cp.TokenSigner()
	if err != nil {
		t.Fatalf("cp.TokenSigner: %v", err)
	}
	token := signer.Create()
	return cp, token
}

// withAuth adds a valid session cookie to a request and returns it.
func withAuth(req *http.Request, token string) *http.Request {
	req.AddCookie(&http.Cookie{Name: sessionCookieName(req), Value: token})
	return req
}
