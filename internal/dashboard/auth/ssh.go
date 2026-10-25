package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// verifySSHSig checks that sig is an SSHSIG (OpenSSH `ssh-keygen -Y sign`) over
// payload, made with operator's private key and verified against the operator's
// configured public key, in the dashboard-enroll namespace. This is the same
// trust root SSH-signed approvals use (internal/bot/approve.go), with its own
// namespace so neither signature can be replayed as the other (design §6.3).
//
// It is the default Deps.VerifySSH; tests inject a fake so the suite needs no
// ssh-keygen and no real key.
func verifySSHSig(ctx context.Context, operator, pubKey string, payload []byte, sig string) error {
	if strings.TrimSpace(sig) == "" {
		return errors.New("no signature")
	}
	if operator == "" || strings.ContainsAny(operator, " \t\n\",") {
		return errors.New("bad operator name")
	}
	if strings.TrimSpace(pubKey) == "" {
		return fmt.Errorf("operator %q has no ssh_key configured", operator)
	}
	dir, err := os.MkdirTemp("", "conductor-enroll-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	allowed := filepath.Join(dir, "allowed_signers")
	line := fmt.Sprintf("%s namespaces=%q %s\n", operator, EnrollNamespace, strings.TrimSpace(pubKey))
	if err := os.WriteFile(allowed, []byte(line), 0o600); err != nil {
		return err
	}
	sigFile := filepath.Join(dir, "sig")
	if err := os.WriteFile(sigFile, []byte(sig), 0o600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "ssh-keygen", "-Y", "verify", "-f", allowed, "-I", operator, "-n", EnrollNamespace, "-s", sigFile)
	cmd.Stdin = bytes.NewReader(payload)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("enrollment signature does not verify for operator %q: %s", operator, strings.TrimSpace(out.String()))
	}
	return nil
}

// b64 encodes bytes as URL-safe base64 without padding (ids, secrets, tokens).
func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
