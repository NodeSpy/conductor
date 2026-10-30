package gitpush

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Signer signs commits with an SSH key, as git's gpg.format=ssh does
// (`ssh-keygen -Y sign -n git`): the signature GitHub verifies against the
// committer's registered SSH signing keys.
type Signer struct {
	// Key is a private key file, or a public key (.pub, or git's
	// "key::ssh-…" literal) whose private half is in the ssh-agent.
	Key string
	// Name / Email replace a re-signed commit's committer ("" = keep).
	Name, Email string
}

// sshKeygen is the ssh-keygen binary (a var for tests).
var sshKeygen = "ssh-keygen"

// sign returns the armored SSH signature over payload.
func (s *Signer) sign(ctx context.Context, payload []byte) (string, error) {
	key := s.Key
	if lit, ok := strings.CutPrefix(key, "key::"); ok {
		f, err := os.CreateTemp("", "conductor-signkey-*.pub")
		if err != nil {
			return "", err
		}
		defer os.Remove(f.Name())
		if _, err := f.WriteString(lit + "\n"); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
		key = f.Name()
	}
	cmd := exec.CommandContext(ctx, sshKeygen, "-Y", "sign", "-n", "git", "-f", key)
	cmd.Stdin = bytes.NewReader(payload)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("sign with %s: %w: %s", filepath.Base(s.Key), err, strings.TrimSpace(errb.String()))
	}
	sig := strings.TrimSpace(out.String())
	if !strings.HasPrefix(sig, "-----BEGIN SSH SIGNATURE-----") {
		return "", fmt.Errorf("sign with %s: ssh-keygen produced no signature", filepath.Base(s.Key))
	}
	return sig, nil
}

// rewriteCommit returns raw (a commit object's content) with its parents
// mapped through parents, any existing signature dropped, the committer
// replaced by the signer's identity (its timestamp kept), and — when s is
// set — a fresh signature over the result.
func rewriteCommit(ctx context.Context, raw []byte, parents map[string]string, s *Signer) ([]byte, error) {
	head, body, ok := bytes.Cut(raw, []byte("\n\n"))
	if !ok {
		// A commit with no message ends its headers at the last newline.
		head, body = bytes.TrimSuffix(raw, []byte("\n")), nil
	}
	var hdr bytes.Buffer
	skipping := false
	for _, line := range strings.Split(string(head), "\n") {
		if strings.HasPrefix(line, " ") {
			if !skipping {
				hdr.WriteString(line + "\n")
			}
			continue
		}
		skipping = false
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "gpgsig", "gpgsig-sha256":
			skipping = true
			continue
		case "parent":
			if np, ok := parents[val]; ok {
				val = np
			}
		case "committer":
			if s != nil && (s.Name != "" || s.Email != "") {
				val = replaceIdent(val, s.Name, s.Email)
			}
		}
		hdr.WriteString(key + " " + val + "\n")
	}
	payload := append(append(append([]byte(nil), hdr.Bytes()...), '\n'), body...)
	if s == nil {
		return payload, nil
	}
	sig, err := s.sign(ctx, payload)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(hdr.Bytes())
	for i, l := range strings.Split(sig, "\n") {
		if i == 0 {
			out.WriteString("gpgsig " + l + "\n")
		} else {
			out.WriteString(" " + l + "\n")
		}
	}
	out.WriteByte('\n')
	out.Write(body)
	return out.Bytes(), nil
}

// replaceIdent swaps the "Name <email>" of an ident line ("Name <email> TS
// TZ"), keeping its timestamp.
func replaceIdent(val, name, email string) string {
	i := strings.LastIndex(val, ">")
	j := strings.Index(val, "<")
	if i < 0 || j < 0 || j > i {
		return val
	}
	curName := strings.TrimSpace(val[:j])
	curEmail := val[j+1 : i]
	if name == "" {
		name = curName
	}
	if email == "" {
		email = curEmail
	}
	clean := func(s string) string { return strings.NewReplacer("<", "", ">", "", "\n", " ").Replace(s) }
	return clean(name) + " <" + clean(email) + ">" + val[i+1:]
}
