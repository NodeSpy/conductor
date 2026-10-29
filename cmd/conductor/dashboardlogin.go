package main

// `conductor dashboard approve-login` and `conductor dashboard sign` — the CLI
// side of SSH sign-in and SSH step-up (dashboard-auth-methods design). Neither
// talks to the daemon's control socket or needs a session: the SSH signature is
// the credential. approve-login is the cross-device path (a phone shows a login
// code; any machine with the operator's key approves it over the dashboard's
// own URL); sign is the paste path (it prints the signature for a challenge the
// page shows).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/dashboard/auth"
)

// dashboardSSHCommand runs approve-login / sign. They don't need a config: with
// --key the key file is enough; without it the operator's configured ssh_key is
// used through the ssh-agent, which needs the config.
func dashboardSSHCommand(sub string, args []string) error {
	var pos []string
	fl := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--yes" || a == "-y":
			fl["yes"] = "true"
		case strings.HasPrefix(a, "--") && strings.Contains(a, "="):
			k, v, _ := strings.Cut(strings.TrimPrefix(a, "--"), "=")
			fl[k] = v
		case strings.HasPrefix(a, "--") && i+1 < len(args):
			fl[strings.TrimPrefix(a, "--")] = args[i+1]
			i++
		case strings.HasPrefix(a, "--"):
			return fmt.Errorf("%s needs a value", a)
		default:
			pos = append(pos, a)
		}
	}
	switch sub {
	case "approve-login":
		if len(pos) != 1 {
			return fmt.Errorf("usage: conductor dashboard approve-login <code> --url <dashboard url> --operator <name> [--key <ssh key>]")
		}
		return dashboardApproveLogin(pos[0], fl)
	default:
		if len(pos) != 1 {
			return fmt.Errorf("usage: conductor dashboard sign '<challenge>' [--key <ssh key>]")
		}
		return dashboardSign(pos[0], fl)
	}
}

// signingKey resolves the key to sign with: --key, else the operator's
// configured public key (its private half in the ssh-agent). The returned
// cleanup removes a temp public-key file.
func signingKey(fl map[string]string, operator string) (string, func(), error) {
	if k := fl["key"]; k != "" {
		return k, func() {}, nil
	}
	var cargs []string
	if c := fl["config"]; c != "" {
		cargs = []string{"--config", c}
	}
	cfg, _, err := loadConfig(cargs)
	if err != nil {
		return "", nil, fmt.Errorf("no --key, and the config (for operators.%s.ssh_key via the ssh-agent) didn't load: %w", operator, err)
	}
	op, ok := cfg.Operators[operator]
	if !ok || strings.TrimSpace(op.SSHKey) == "" {
		return "", nil, fmt.Errorf("operator %q has no ssh_key in this config — pass --key <private key file>", operator)
	}
	f, err := os.CreateTemp("", "conductor-operator-*.pub")
	if err != nil {
		return "", nil, err
	}
	if _, err := f.WriteString(strings.TrimSpace(op.SSHKey) + "\n"); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

// dashboardSign prints the SSHSIG for a challenge the dashboard shows (paste
// flow). It only signs dashboard login/step-up challenges, in their own
// namespaces.
func dashboardSign(challenge string, fl map[string]string) error {
	p, err := auth.ParseSSHChallenge(challenge)
	if err != nil {
		return err
	}
	key, cleanup, err := signingKey(fl, p.Fields["operator"])
	if err != nil {
		return err
	}
	defer cleanup()
	sig, err := auth.SignSSH(context.Background(), key, p.Namespace, []byte(strings.TrimSpace(challenge)))
	if err != nil {
		return err
	}
	fmt.Print(sig)
	return nil
}

// canonicalOrigin is scheme://host[:port] lowercased, without a default port.
func canonicalOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%q is not a dashboard URL (http[s]://host[:port])", raw)
	}
	host := strings.ToLower(u.Host)
	if (u.Scheme == "https" && strings.HasSuffix(host, ":443")) || (u.Scheme == "http" && strings.HasSuffix(host, ":80")) {
		host = host[:strings.LastIndex(host, ":")]
	}
	return u.Scheme + "://" + host, nil
}

// dashboardApproveLogin approves a pending sign-in or step-up by its login code:
// it fetches what the code stands for, shows it, checks the challenge is for
// THIS operator on THIS dashboard URL (so a look-alike server can't relay
// another dashboard's challenge for us to sign), signs it and posts the
// signature. The page that showed the code then receives its session.
func dashboardApproveLogin(code string, fl map[string]string) error {
	op := fl["operator"]
	if op == "" {
		return fmt.Errorf("say which operator with --operator <name>")
	}
	origin, err := canonicalOrigin(fl["url"])
	if err != nil {
		return fmt.Errorf("--url: %w", err)
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	var pend auth.SSHPendingView
	if err := postJSON(hc, origin+"/api/v1/auth/ssh/pending", map[string]string{"code": code}, &pend); err != nil {
		return err
	}
	p, err := auth.ParseSSHChallenge(pend.Challenge)
	if err != nil {
		return fmt.Errorf("the dashboard returned an unexpected challenge: %w", err)
	}
	chOrigin, err := canonicalOrigin(p.Fields["origin"])
	if err != nil || chOrigin != origin {
		return fmt.Errorf("refusing: the challenge is for %s, not %s — run approve-login against the URL the waiting page is on", p.Fields["origin"], origin)
	}
	if p.Fields["operator"] != op || pend.Operator != op {
		return fmt.Errorf("refusing: the request is for operator %q, not %q", p.Fields["operator"], op)
	}
	if p.Namespace != pend.Namespace {
		return fmt.Errorf("refusing: the challenge's namespace doesn't match its kind")
	}
	what := "Sign-in"
	if p.Kind == "stepup" {
		what = fmt.Sprintf("Confirmation (%s)", p.Fields["purpose"])
	}
	fmt.Fprintf(os.Stderr, "%s request for operator %s\n  from:    %s (%s)\n  on:      %s\n  expires: %s\n",
		what, op, pend.Device, pend.Addr, origin, pend.Expires)
	if fl["yes"] != "true" {
		if !confirmTTY("Approve it? Only if you started it yourself just now. [y/N] ") {
			return fmt.Errorf("not approved")
		}
	}
	key, cleanup, err := signingKey(fl, op)
	if err != nil {
		return err
	}
	defer cleanup()
	sig, err := auth.SignSSH(context.Background(), key, p.Namespace, []byte(pend.Challenge))
	if err != nil {
		return err
	}
	if err := postJSON(hc, origin+"/api/v1/auth/ssh/approve", map[string]string{"code": code, "signature": sig}, nil); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "approved — the waiting page continues now")
	return nil
}

// confirmTTY asks a yes/no question on the terminal (stdin when there's no tty).
func confirmTTY(prompt string) bool {
	in := io.Reader(os.Stdin)
	if tty, err := os.Open("/dev/tty"); err == nil {
		defer tty.Close()
		in = tty
	}
	fmt.Fprint(os.Stderr, prompt)
	line, _ := bufio.NewReader(in).ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes"
}

// postJSON posts body and decodes a 2xx JSON reply into out (when non-nil), or
// returns the dashboard's error message.
func postJSON(hc *http.Client, u string, body any, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("reach the dashboard: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			return fmt.Errorf("dashboard: %s", e.Error.Message)
		}
		return fmt.Errorf("dashboard: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
