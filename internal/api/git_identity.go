package api

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/nchapman/hiro/internal/platform/fsperm"
)

const (
	gitConfigFile = ".gitconfig"
	sshDir        = ".ssh"
	sshPrivKey    = "id_ed25519"
	sshPubKey     = "id_ed25519.pub"
	maxNameLen    = 128
	maxEmailLen   = 254
)

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

type gitIdentityResponse struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	HasKey   bool   `json:"has_key"`
	Pubkey   string `json:"pubkey,omitempty"`
	Hostname string `json:"hostname"`
}

type gitIdentityPutRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type sshKeyPostRequest struct {
	Force bool `json:"force,omitempty"`
}

type sshKeyResponse struct {
	Pubkey string `json:"pubkey"`
}

func (s *Server) handleGetGitIdentity(w http.ResponseWriter, _ *http.Request) {
	name, email, _ := readGitUser(filepath.Join(s.rootDir, gitConfigFile))

	pubkey, hasKey := readPubkey(filepath.Join(s.rootDir, sshDir, sshPubKey))

	host, _ := os.Hostname()
	writeJSON(w, http.StatusOK, gitIdentityResponse{
		Name:     name,
		Email:    email,
		HasKey:   hasKey,
		Pubkey:   pubkey,
		Hostname: host,
	})
}

func (s *Server) handlePutGitIdentity(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodySize)
	var req gitIdentityPutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(req.Email)

	if req.Name == "" || len(req.Name) > maxNameLen {
		http.Error(w, "name must be 1-128 characters", http.StatusBadRequest)
		return
	}
	// gitconfig is line-oriented; an embedded CR/LF in a value would terminate
	// the line and let an attacker inject arbitrary sections like
	// [core] sshCommand = ... which git executes on the next git operation.
	if strings.ContainsAny(req.Name, "\r\n") {
		http.Error(w, "name must not contain newlines", http.StatusBadRequest)
		return
	}
	if req.Email == "" || len(req.Email) > maxEmailLen || !emailRe.MatchString(req.Email) {
		http.Error(w, "invalid email", http.StatusBadRequest)
		return
	}

	path := filepath.Join(s.rootDir, gitConfigFile)
	if err := writeGitUser(path, req.Name, req.Email); err != nil {
		s.logger.Error("failed to write .gitconfig", "error", err)
		http.Error(w, "failed to write .gitconfig", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handlePostSSHKey(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodySize)
	var req sshKeyPostRequest
	// Body is optional.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
	}

	dir := filepath.Join(s.rootDir, sshDir)
	privPath := filepath.Join(dir, sshPrivKey)
	pubPath := filepath.Join(dir, sshPubKey)

	if !req.Force {
		if pub, ok := readPubkey(pubPath); ok {
			writeJSON(w, http.StatusOK, sshKeyResponse{Pubkey: pub})
			return
		}
	}

	host, _ := os.Hostname()
	if host == "" {
		host = "hiro"
	}
	comment := "hiro@" + host

	pub, err := generateSSHKey(dir, privPath, pubPath, comment)
	if err != nil {
		s.logger.Error("failed to generate ssh key", "error", err)
		http.Error(w, "failed to generate ssh key", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, sshKeyResponse{Pubkey: pub})
}

// generateSSHKey writes a new ed25519 keypair to dir in OpenSSH format.
// Returns the pubkey as an authorized_keys line (trimmed).
func generateSSHKey(dir, privPath, pubPath, comment string) (string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate ed25519: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("new ssh public key: %w", err)
	}
	pubLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + comment

	privBlock, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", fmt.Errorf("marshal private key: %w", err)
	}
	privPEM := pem.EncodeToMemory(privBlock)

	if err := os.MkdirAll(dir, fsperm.DirPrivate); err != nil {
		return "", fmt.Errorf("mkdir .ssh: %w", err)
	}
	if err := os.WriteFile(privPath, privPEM, fsperm.FilePrivate); err != nil {
		return "", fmt.Errorf("write private key: %w", err)
	}
	if err := os.WriteFile(pubPath, []byte(pubLine+"\n"), fsperm.FilePrivate); err != nil {
		return "", fmt.Errorf("write public key: %w", err)
	}
	return pubLine, nil
}

func readPubkey(path string) (string, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // path under rootDir
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(data))
	return s, s != ""
}

// readGitUser parses [user] name/email from a gitconfig file. Unknown sections
// and keys are ignored. Returns empty strings if the file or section is missing.
func readGitUser(path string) (name, email string, err error) {
	f, err := os.Open(path) //nolint:gosec // path under rootDir
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", nil
		}
		return "", "", err
	}
	defer f.Close()

	inUser := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.TrimSpace(strings.Trim(line, "[]"))
			inUser = strings.EqualFold(section, "user")
			continue
		}
		if !inUser {
			continue
		}
		keyPart, valPart, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k := strings.TrimSpace(keyPart)
		v := strings.Trim(strings.TrimSpace(valPart), `"`)
		switch strings.ToLower(k) {
		case "name":
			name = v
		case "email":
			email = v
		}
	}
	return name, email, sc.Err()
}

// writeGitUser updates the [user] section of a gitconfig, preserving all other
// sections and keys. Creates the file if missing.
func writeGitUser(path, name, email string) error {
	var existing []byte
	if data, err := os.ReadFile(path); err == nil { //nolint:gosec // path under rootDir
		existing = data
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	updated := updateGitUserSection(existing, name, email)
	return os.WriteFile(path, updated, fsperm.FilePrivate)
}

// updateGitUserSection takes the raw contents of a gitconfig and returns the
// same contents with [user].name and [user].email set to the provided values.
// If the [user] section exists, name/email are updated in place (added if
// missing). If it doesn't exist, a new section is appended.
func updateGitUserSection(src []byte, name, email string) []byte {
	if len(src) == 0 {
		return fmt.Appendf(nil, "[user]\n\tname = %s\n\temail = %s\n", name, email)
	}

	lines := splitLinesKeepEOL(src)
	var out bytes.Buffer

	inUser := false
	foundUser := false
	wroteName := false
	wroteEmail := false

	// flushPending writes any missing name/email keys before leaving the [user] section.
	flushPending := func() {
		if inUser && foundUser {
			if !wroteName {
				fmt.Fprintf(&out, "\tname = %s\n", name)
				wroteName = true
			}
			if !wroteEmail {
				fmt.Fprintf(&out, "\temail = %s\n", email)
				wroteEmail = true
			}
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isSectionHeader(trimmed) {
			flushPending()
			inUser = strings.EqualFold(sectionName(trimmed), "user")
			if inUser {
				foundUser = true
				// Reset per-section so flushPending appends missing keys to
				// each [user] section it encounters.
				wroteName = false
				wroteEmail = false
			}
			out.WriteString(line)
			continue
		}
		if inUser && !isComment(trimmed) {
			if handled := rewriteUserKey(&out, trimmed, name, email, &wroteName, &wroteEmail); handled {
				continue
			}
		}
		out.WriteString(line)
	}
	// Flush at EOF if still inside [user].
	flushPending()

	if !foundUser {
		// Append a new [user] section.
		if out.Len() > 0 && !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "[user]\n\tname = %s\n\temail = %s\n", name, email)
	}
	return out.Bytes()
}

func isSectionHeader(trimmed string) bool {
	return strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")
}

func sectionName(trimmed string) string {
	return strings.TrimSpace(strings.Trim(trimmed, "[]"))
}

func isComment(trimmed string) bool {
	return strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";")
}

// rewriteUserKey writes a replacement line for [user].name/email if trimmed is
// one of those keys, and reports whether it handled the line.
func rewriteUserKey(out *bytes.Buffer, trimmed, name, email string, wroteName, wroteEmail *bool) bool {
	keyPart, _, ok := strings.Cut(trimmed, "=")
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(keyPart)) {
	case "name":
		fmt.Fprintf(out, "\tname = %s\n", name)
		*wroteName = true
		return true
	case "email":
		fmt.Fprintf(out, "\temail = %s\n", email)
		*wroteEmail = true
		return true
	}
	return false
}

// splitLinesKeepEOL splits src into lines, preserving trailing newlines on each line.
func splitLinesKeepEOL(src []byte) []string {
	var lines []string
	start := 0
	for i, b := range src {
		if b == '\n' {
			lines = append(lines, string(src[start:i+1]))
			start = i + 1
		}
	}
	if start < len(src) {
		lines = append(lines, string(src[start:]))
	}
	return lines
}
