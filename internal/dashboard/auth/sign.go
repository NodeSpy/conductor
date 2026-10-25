package auth

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SignEnrollTicket signs an EnrollPayload with ssh-keygen, the CLI side of
// enrollment (mirrors bot.SignApproval). key is a private key file, or a PUBLIC
// key file whose private half is held by the ssh-agent (a confirming agent or a
// hardware key then asks the human). It signs in the dashboard-enroll namespace
// so the signature can only ever authorize an enrollment.
func SignEnrollTicket(ctx context.Context, key string, payload []byte) (string, error) {
	dir, err := os.MkdirTemp("", "conductor-enroll-sign-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	msg := filepath.Join(dir, "payload")
	if err := os.WriteFile(msg, payload, 0o600); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "ssh-keygen", "-Y", "sign", "-q", "-f", key, "-n", EnrollNamespace, msg)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdin = os.Stdin // a passphrase / confirmation prompt reaches the operator
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ssh-keygen sign: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	sig, err := os.ReadFile(msg + ".sig")
	if err != nil {
		return "", err
	}
	return string(sig), nil
}
