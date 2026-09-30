package jail

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/NodeSpy/conductor/internal/sshsig"
)

// Commit signing (#154 §4). Inside the jail git's signing program is
// conductor's shim (gpg.ssh.program / gpg.program, per gpg.format). The shim
// sends the payload; conductor checks it is a commit object for this
// dispatch's repository — a tree and parents that exist in its object store
// — and signs it outside the jail with the operator's configured
// user.signingkey. The key never enters the jail and is never read by
// conductor (ssh-keygen or gpg reads it, as for the operator's own commits);
// commits show Verified. The limit is inherent and deliberate: the agent can
// get content IT wrote signed as the operator — that is what committing as
// them means — but nothing else (no tags, no arbitrary blobs, no other
// namespace than git's).

// commitHeader is a parsed commit payload header.
type commitHeader struct {
	Tree    string
	Parents []string
	Subject string
}

// parseCommitPayload accepts exactly a git commit object's text (the
// payload git hands the signing program): tree, parents, author, committer,
// optional encoding/mergetag, a blank line, the message. Anything else — a
// tag, a push certificate, a blob — is refused.
func parseCommitPayload(p []byte) (commitHeader, error) {
	var h commitHeader
	sc := bufio.NewScanner(bytes.NewReader(p))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	first := true
	inBody := false
	sawAuthor, sawCommitter := false, false
	for sc.Scan() {
		line := sc.Text()
		if inBody {
			if h.Subject == "" && strings.TrimSpace(line) != "" {
				h.Subject = line
			}
			continue
		}
		if line == "" {
			inBody = true
			continue
		}
		if strings.HasPrefix(line, " ") {
			continue // continuation of a multi-line header (mergetag)
		}
		k, v, _ := strings.Cut(line, " ")
		if first {
			if k != "tree" || !isHex(v) {
				return h, fmt.Errorf("not a commit object")
			}
			h.Tree = v
			first = false
			continue
		}
		switch k {
		case "parent":
			if !isHex(v) {
				return h, fmt.Errorf("bad parent")
			}
			h.Parents = append(h.Parents, v)
		case "author":
			sawAuthor = true
		case "committer":
			sawCommitter = true
		case "encoding", "mergetag":
		case "gpgsig", "gpgsig-sha256":
			return h, fmt.Errorf("payload is already signed")
		default:
			return h, fmt.Errorf("unexpected commit header %q", k)
		}
	}
	if first || !sawAuthor || !sawCommitter || !inBody {
		return h, fmt.Errorf("not a commit object")
	}
	return h, nil
}

func (m *Manager) handleSign(ctx context.Context, d *Dispatch, req Request, fw *frameWriter) {
	refuse := func(reason string) {
		m.emit(d, Event{Type: "sign", Status: "refused", Reason: reason})
		_ = fw.send(Reply{Done: true, Exit: 1, Refused: reason})
	}
	h, err := parseCommitPayload(req.Payload)
	if err != nil {
		refuse("sign: " + err.Error() + " — only this dispatch's commits are signed")
		return
	}
	if d.Git == nil || d.Git.CommonDir == "" {
		refuse("sign: this dispatch has no repository")
		return
	}
	for _, oid := range append([]string{h.Tree}, h.Parents...) {
		if _, _, err := dgit(ctx, d, true, "cat-file", "-e", oid); err != nil {
			refuse("sign: " + oid[:min(12, len(oid))] + " is not in the dispatch's repository — only this dispatch's commits are signed")
			return
		}
	}
	signer := m.Signer
	if signer == nil {
		signer = operatorSign
	}
	sig, status, err := signer(ctx, d, req.Format, req.Payload)
	if err != nil {
		m.emit(d, Event{Type: "sign", Status: "error", Detail: h.Subject, Reason: err.Error()})
		_ = fw.send(Reply{Done: true, Exit: 1, Error: "conductor: signing failed: " + err.Error()})
		return
	}
	m.emit(d, Event{Type: "sign", Status: "ok", Detail: "git commit " + shortSubject(h.Subject) + " (tree " + h.Tree[:12] + ")"})
	_ = fw.send(Reply{Done: true, Sig: sig, Status: status})
}

func shortSubject(s string) string {
	if len(s) > 72 {
		return s[:72] + "…"
	}
	return fmt.Sprintf("%q", s)
}

// operatorSign signs payload the way the operator's own git would: the
// configured gpg.format, program, and user.signingkey, read from TRUSTED
// config only — global plus conductor's own base clone (or, for another
// checkout shape, the common dir the jail can only read). Never the
// dispatch clone's own config: the agent writes that, and gpg.ssh.program
// names a program conductor would run.
func operatorSign(ctx context.Context, d *Dispatch, _ string, payload []byte) ([]byte, []byte, error) {
	cd := d.Git.trustedGitDir()
	format := gitConfigGet(cd, "gpg.format")
	key := gitConfigGet(cd, "user.signingkey")
	switch format {
	case "ssh":
		if key == "" {
			return nil, nil, fmt.Errorf("gpg.format is ssh but user.signingkey is unset")
		}
		prog := gitConfigGet(cd, "gpg.ssh.program")
		if prog == "" {
			prog = "ssh-keygen"
		}
		if _, err := exec.LookPath(prog); err != nil && prog == "ssh-keygen" {
			// No ssh-keygen on this box: sign natively (SSHSIG).
			s, err := sshsig.LoadSigner(key, "")
			if err != nil {
				return nil, nil, err
			}
			sig, err := sshsig.Sign(s, "git", payload)
			return sig, nil, err
		}
		return sshKeygenSign(ctx, d, prog, key, payload)
	case "x509":
		return nil, nil, fmt.Errorf("gpg.format x509 is not brokered")
	default:
		prog := firstNonEmpty(gitConfigGet(cd, "gpg.openpgp.program"), gitConfigGet(cd, "gpg.program"), "gpg")
		if key == "" {
			key = committerIdent(payload)
		}
		cmd := exec.CommandContext(ctx, prog, "--status-fd=2", "-bsau", key)
		cmd.Stdin = bytes.NewReader(payload)
		var out, st bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &st
		if err := cmd.Run(); err != nil {
			return nil, nil, fmt.Errorf("%s: %v", prog, firstLine(st.String(), err))
		}
		return out.Bytes(), st.Bytes(), nil
	}
}

// sshKeygenSign runs `<prog> -Y sign -n git -f <key> <file>` on the host —
// the operator's key file (or agent) is used in place by ssh-keygen.
func sshKeygenSign(ctx context.Context, d *Dispatch, prog, key string, payload []byte) ([]byte, []byte, error) {
	dir, err := os.MkdirTemp(d.Dir, "sign-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	buf := filepath.Join(dir, "payload")
	if err := os.WriteFile(buf, payload, 0o600); err != nil {
		return nil, nil, err
	}
	args := []string{"-Y", "sign", "-n", "git"}
	if lit, ok := literalSSHKey(key); ok {
		kf := filepath.Join(dir, "key.pub")
		if err := os.WriteFile(kf, []byte(lit+"\n"), 0o600); err != nil {
			return nil, nil, err
		}
		args = append(args, "-f", kf, "-U")
	} else {
		args = append(args, "-f", expandTilde(key))
	}
	args = append(args, buf)
	cmd := exec.CommandContext(ctx, prog, args...)
	var st bytes.Buffer
	cmd.Stderr = &st
	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("%s -Y sign: %s", filepath.Base(prog), firstLine(st.String(), err))
	}
	sig, err := os.ReadFile(buf + ".sig")
	return sig, nil, err
}

func literalSSHKey(k string) (string, bool) {
	if s, ok := strings.CutPrefix(k, "key::"); ok {
		return s, true
	}
	if strings.HasPrefix(k, "ssh-") || strings.HasPrefix(k, "ecdsa-") || strings.HasPrefix(k, "sk-") {
		return k, true
	}
	return "", false
}

func expandTilde(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

func committerIdent(payload []byte) string {
	for _, line := range strings.Split(string(payload), "\n") {
		if v, ok := strings.CutPrefix(line, "committer "); ok {
			if i := strings.LastIndex(v, ">"); i >= 0 {
				return v[:i+1]
			}
		}
		if line == "" {
			break
		}
	}
	return ""
}

// ---- in-jail shims -----------------------------------------------------------

// RunSSHSignShim is gpg.ssh.program inside the jail: `-Y sign` goes to the
// broker; verification (`-Y verify`, find-principals, check-novalidate)
// needs no secret and runs the real ssh-keygen in the jail.
func RunSSHSignShim(args []string) int {
	sign := false
	for i, a := range args {
		if a == "-Y" && i+1 < len(args) && args[i+1] == "sign" {
			sign = true
		}
	}
	if !sign {
		return execNative("ssh-keygen", args)
	}
	file := args[len(args)-1]
	payload, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "conductor: sign: %v\n", err)
		return 1
	}
	rep, err := Call(Request{Op: "sign", Format: "ssh", Payload: payload}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "conductor: sign: %v\n", err)
		return 1
	}
	if rep.Refused != "" || rep.Error != "" {
		fmt.Fprintf(os.Stderr, "conductor: sign refused: %s\n", firstNonEmpty(rep.Refused, rep.Error))
		return 1
	}
	if err := os.WriteFile(file+".sig", rep.Sig, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "conductor: sign: %v\n", err)
		return 1
	}
	return 0
}

// RunGPGSignShim is gpg.program inside the jail: a detached-sign request
// (`--status-fd=2 -bsau <key>`, payload on stdin) goes to the broker; any
// other use (verification) runs the real gpg in the jail.
func RunGPGSignShim(args []string) int {
	sign := false
	for _, a := range args {
		if a == "--detach-sign" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "b") && strings.Contains(a, "s")) {
			sign = true
		}
	}
	if !sign {
		return execNative("gpg", args)
	}
	payload, err := io.ReadAll(io.LimitReader(os.Stdin, 16<<20))
	if err != nil {
		fmt.Fprintf(os.Stderr, "conductor: sign: %v\n", err)
		return 1
	}
	rep, err := Call(Request{Op: "sign", Format: "openpgp", Payload: payload}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "conductor: sign: %v\n", err)
		return 1
	}
	if rep.Refused != "" || rep.Error != "" {
		fmt.Fprintf(os.Stderr, "conductor: sign refused: %s\n", firstNonEmpty(rep.Refused, rep.Error))
		return 1
	}
	_, _ = os.Stdout.Write(rep.Sig)
	_, _ = os.Stderr.Write(rep.Status)
	return 0
}
