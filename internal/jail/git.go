package jail

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Brokered git (#154 §4). Inside the jail, local git runs natively; its
// network side cannot: GitHub remotes (and the base clone's own origin) are
// rewritten — through GIT_CONFIG_* env, no file touched — to
// `conductor::owner/repo`, so every fetch, push, lazy blob fetch, alias or
// script that needs the network invokes git-remote-conductor (conductor's
// binary), which asks the broker. conductor then runs the fetch/push itself
// from the shared base clone, with the operator's identity, after policy:
// the dispatch's repository only; pushes only to the dispatch's own branch —
// never the default branch, no force, no deletion unless allowed; review
// steps no push at all. The objects are already in the shared object store,
// so the daemon needs no pack from the jail.

// gitEnv is the jail's git configuration, as GIT_CONFIG_* variables (the
// highest-precedence source short of `-c`, and invisible on disk).
func gitEnv(d *Dispatch) []string {
	type kv struct{ k, v string }
	kvs := []kv{
		{"url.conductor::.insteadOf", "git@github.com:"},
		{"url.conductor::.insteadOf", "ssh://git@github.com/"},
		{"url.conductor::.insteadOf", "https://github.com/"},
		{"url.conductor::.insteadOf", "git://github.com/"},
		{"gpg.ssh.program", BinDir + "/" + ShimSSHSign},
		{"gpg.program", BinDir + "/" + ShimGPGSign},
		{"gpg.openpgp.program", BinDir + "/" + ShimGPGSign},
		{"gpg.x509.program", BinDir + "/" + ShimGPGSign},
		// No credential helper in the jail (the operator's `gh auth
		// git-credential` would only reach the gh shim and be refused).
		{"credential.helper", ""},
		// The object store is shared with the daemon and other dispatches:
		// no automatic gc/maintenance from inside a jail.
		{"gc.auto", "0"},
		{"maintenance.auto", "false"},
	}
	if d.Git != nil && d.Git.OriginURL != "" && d.Repo != "" {
		kvs = append(kvs, kv{"url.conductor::" + d.Repo + ".insteadOf", d.Git.OriginURL})
	}
	out := []string{fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(kvs))}
	for i, e := range kvs {
		out = append(out, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, e.k), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, e.v))
	}
	return append(out, "GIT_TERMINAL_PROMPT=0")
}

// hostGit runs git on the host against the dispatch's common dir, hardened:
// the git dir is named explicitly (the worktree's .git pointer and gitdir
// files are agent-writable, never trusted), hooks and fsmonitor are off, the
// ext:: transport is refused, and replace refs are ignored — so even a
// tampered clone cannot make the daemon execute anything.
func hostGit(ctx context.Context, commonDir string, stdin []byte, args ...string) (string, string, error) {
	full := append([]string{"--git-dir=" + commonDir,
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
		"-c", "protocol.ext.allow=never", "-c", "diff.external=",
		"-c", "core.sshCommand=" + trustedSSHCommand()}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1",
		"GIT_CONFIG_NOSYSTEM=", "GIT_DIR=", "GIT_WORK_TREE=")
	cmd.Env = scrubGitEnv(cmd.Env)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// scrubGitEnv drops inherited git-steering variables (a daemon started from
// a git hook would carry GIT_DIR; GIT_CONFIG_* would inject config).
func scrubGitEnv(env []string) []string {
	out := env[:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == "GIT_DIR" || k == "GIT_WORK_TREE" || k == "GIT_INDEX_FILE" || k == "GIT_COMMON_DIR" ||
			k == "GIT_OBJECT_DIRECTORY" || k == "GIT_ALTERNATE_OBJECT_DIRECTORIES" ||
			strings.HasPrefix(k, "GIT_CONFIG_") || k == "GIT_CONFIG" || k == "GIT_EXEC_PATH" || k == "GIT_SSH" || k == "GIT_SSH_COMMAND" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1")
}

// trustedSSHCommand is the ssh command from TRUSTED config only (global,
// then system), never a repository's local config.
var trustedSSHCommand = func() string {
	for _, scope := range []string{"--global", "--system"} {
		out, err := exec.Command("git", "config", scope, "--get", "core.sshCommand").Output()
		if err == nil {
			if s := strings.TrimSpace(string(out)); s != "" {
				return s
			}
		}
	}
	return "ssh"
}

func gitConfigGet(commonDir, key string) string {
	out, _, err := hostGit(context.Background(), commonDir, nil, "config", "--get", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// normRemote turns a helper URL ("acme/app.git", "acme/app/") into owner/repo.
func normRemote(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimPrefix(u, "conductor::")
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	return u
}

func (m *Manager) checkRemote(d *Dispatch, req Request) string {
	if d.Git == nil || d.Git.CommonDir == "" {
		return "git: this dispatch has no repository checkout"
	}
	r := normRemote(req.Remote)
	if !strings.EqualFold(r, d.Repo) {
		return fmt.Sprintf("git: remote %s is not the dispatch's repository %s", r, d.Repo)
	}
	return ""
}

func (m *Manager) gitList(ctx context.Context, d *Dispatch, req Request, fw *frameWriter) {
	if r := m.checkRemote(d, req); r != "" {
		m.emit(d, Event{Type: "git_fetch", Status: "refused", Detail: "ls-remote " + req.Remote, Reason: r})
		_ = fw.send(Reply{Done: true, Refused: r, Exit: 1})
		return
	}
	out, stderr, err := hostGit(ctx, d.Git.CommonDir, nil, "ls-remote", "--symref", "origin")
	if err != nil {
		_ = fw.send(Reply{Done: true, Exit: 1, Error: "git ls-remote: " + firstLine(stderr, err)})
		return
	}
	var refs []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "ref: ") {
			target, name, _ := strings.Cut(strings.TrimPrefix(line, "ref: "), "\t")
			refs = append(refs, "@"+target+" "+name)
			continue
		}
		sha, name, ok := strings.Cut(line, "\t")
		if ok {
			refs = append(refs, sha+" "+name)
		}
	}
	_ = fw.send(Reply{Done: true, Refs: refs})
}

func (m *Manager) gitFetch(ctx context.Context, d *Dispatch, req Request, fw *frameWriter) {
	if r := m.checkRemote(d, req); r != "" {
		m.emit(d, Event{Type: "git_fetch", Status: "refused", Detail: "fetch " + req.Remote, Reason: r})
		_ = fw.send(Reply{Done: true, Refused: r, Exit: 1})
		return
	}
	args := []string{"-c", "fetch.negotiationAlgorithm=noop", "fetch", "--no-tags", "--no-write-fetch-head", "--recurse-submodules=no", "--quiet"}
	if req.Filter != "" && validFilter(req.Filter) {
		args = append(args, "--filter="+req.Filter)
	}
	args = append(args, "origin")
	var names []string
	for _, w := range req.Wants {
		switch {
		case strings.HasPrefix(w.Name, "refs/") && !strings.ContainsAny(w.Name, " \t\n:^~?*[\\"):
			args = append(args, w.Name)
			names = append(names, w.Name)
		case isHex(w.SHA):
			args = append(args, w.SHA)
			names = append(names, w.SHA[:min(len(w.SHA), 12)])
		}
	}
	_, stderr, err := hostGit(ctx, d.Git.CommonDir, nil, args...)
	detail := "git fetch origin " + strings.Join(names, " ")
	if len(detail) > 300 {
		detail = detail[:300] + "…"
	}
	if err != nil {
		m.emit(d, Event{Type: "git_fetch", Status: "error", Detail: detail, Reason: firstLine(stderr, err)})
		_ = fw.send(Reply{Done: true, Exit: 1, Error: "git fetch: " + firstLine(stderr, err)})
		return
	}
	m.emit(d, Event{Type: "git_fetch", Status: "ok", Detail: detail})
	_ = fw.send(Reply{Done: true})
}

func (m *Manager) gitPush(ctx context.Context, d *Dispatch, req Request, fw *frameWriter) {
	results := map[string]string{}
	if r := m.checkRemote(d, req); r != "" {
		for _, p := range req.Pushes {
			results[p.Dst] = "error " + r
		}
		m.emit(d, Event{Type: "git_push", Status: "refused", Detail: "push " + req.Remote, Reason: r})
		_ = fw.send(Reply{Done: true, Results: results})
		return
	}
	for _, p := range req.Pushes {
		results[p.Dst] = m.pushOne(ctx, d, p, req.Opts["dry-run"] == "true")
	}
	_ = fw.send(Reply{Done: true, Results: results})
}

// pushOne applies push policy to one ref update and performs it from the
// base clone. The reply is the remote-helper status: "ok" or "error <why>".
func (m *Manager) pushOne(ctx context.Context, d *Dispatch, p GitPush, dry bool) string {
	branch, isBranch := strings.CutPrefix(p.Dst, "refs/heads/")
	del := p.SHA == ""
	detail := "git push origin " + p.Dst
	refuse := func(reason string) string {
		m.emit(d, Event{Type: "git_push", Status: "refused", Detail: detail, Reason: reason})
		return "error " + reason
	}
	if !isBranch {
		return refuse("git: only branch pushes are brokered (" + p.Dst + ")")
	}
	if d.ReadOnly {
		return refuse("target: this is a review step — pushes are refused")
	}
	if m.CheckPush == nil {
		return refuse("target: pushes are not permitted (no target binding wired)")
	}
	if r := m.CheckPush(d, branch, p.Force, del); r != "" {
		return refuse(r)
	}
	if !del && !isHex(p.SHA) {
		return refuse("git: bad source object")
	}
	if !del {
		if _, _, err := hostGit(ctx, d.Git.CommonDir, nil, "cat-file", "-e", p.SHA+"^{commit}"); err != nil {
			return refuse("git: " + p.SHA + " is not a commit in the dispatch's repository")
		}
	}
	spec := p.SHA + ":" + p.Dst
	if del {
		spec = ":" + p.Dst
	} else if p.Force {
		spec = "+" + spec
	}
	args := []string{"push", "--porcelain", "--no-verify"}
	if dry {
		args = append(args, "--dry-run")
	}
	args = append(args, "origin", spec)
	out, stderr, err := hostGit(ctx, d.Git.CommonDir, nil, args...)
	summary := porcelainSummary(out, p.Dst)
	if err != nil {
		why := firstLine(stderr, err)
		if summary != "" {
			why = summary
		}
		m.emit(d, Event{Type: "git_push", Status: "rejected", Detail: detail, Reason: why})
		return "error " + why
	}
	m.emit(d, Event{Type: "git_push", Status: "ok", Detail: detail + " " + summary,
		Fields: map[string]any{"ref": p.Dst, "sha": p.SHA, "summary": summary}})
	return "ok"
}

// porcelainSummary pulls the "old..new" summary for dst out of
// `git push --porcelain` output.
func porcelainSummary(out, dst string) string {
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) >= 3 && strings.HasSuffix(parts[1], ":"+dst) {
			return strings.TrimSpace(parts[2])
		}
	}
	return ""
}

func validFilter(f string) bool {
	return f == "blob:none" || strings.HasPrefix(f, "blob:limit=") || strings.HasPrefix(f, "tree:")
}

func isHex(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func firstLine(stderr string, err error) string {
	s := strings.TrimSpace(stderr)
	if s == "" && err != nil {
		s = err.Error()
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// ---- the in-jail side: git-remote-conductor ---------------------------------

// RunRemoteHelper speaks git's remote-helper protocol (gitremote-helpers(7))
// on stdin/stdout for `git-remote-conductor <remote> <url>`, forwarding the
// network operations to the broker.
func RunRemoteHelper(args []string, in io.Reader, out io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "git-remote-conductor: usage: git-remote-conductor <remote> <url>")
		return 2
	}
	url := args[1]
	opts := map[string]string{}
	r := bufio.NewReader(in)
	w := bufio.NewWriter(out)
	flush := func() { _ = w.Flush() }
	readLine := func() (string, bool) {
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return "", false
		}
		return strings.TrimRight(line, "\n"), true
	}
	for {
		line, ok := readLine()
		if !ok {
			return 0
		}
		switch {
		case line == "":
			return 0
		case line == "capabilities":
			fmt.Fprint(w, "option\nfetch\npush\n\n")
			flush()
		case strings.HasPrefix(line, "option "):
			k, v, _ := strings.Cut(strings.TrimPrefix(line, "option "), " ")
			switch k {
			case "verbosity", "progress", "followtags", "dry-run", "filter", "cloning", "update-shallow", "atomic", "check-connectivity", "force":
				opts[k] = v
				fmt.Fprint(w, "ok\n")
			default:
				fmt.Fprint(w, "unsupported\n")
			}
			flush()
		case line == "list" || line == "list for-push":
			rep, err := Call(Request{Op: "git_list", Remote: url}, nil)
			if err != nil || rep.Refused != "" || rep.Error != "" {
				fmt.Fprintln(os.Stderr, "fatal: conductor: "+firstNonEmpty(rep.Refused, rep.Error, errString(err)))
				return 128
			}
			for _, ref := range rep.Refs {
				fmt.Fprintln(w, ref)
			}
			fmt.Fprint(w, "\n")
			flush()
		case strings.HasPrefix(line, "fetch "):
			var wants []GitWant
			for l := line; strings.HasPrefix(l, "fetch "); {
				f := strings.Fields(strings.TrimPrefix(l, "fetch "))
				if len(f) >= 2 {
					wants = append(wants, GitWant{SHA: f[0], Name: f[1]})
				}
				next, ok := readLine()
				if !ok || next == "" {
					break
				}
				l = next
			}
			rep, err := Call(Request{Op: "git_fetch", Remote: url, Wants: wants, Filter: opts["filter"]}, nil)
			if err != nil || rep.Refused != "" || rep.Error != "" {
				fmt.Fprintln(os.Stderr, "fatal: conductor: "+firstNonEmpty(rep.Refused, rep.Error, errString(err)))
				return 128
			}
			fmt.Fprint(w, "\n")
			flush()
		case strings.HasPrefix(line, "push "):
			var pushes []GitPush
			for l := line; strings.HasPrefix(l, "push "); {
				pushes = append(pushes, parsePushSpec(strings.TrimPrefix(l, "push ")))
				next, ok := readLine()
				if !ok || next == "" {
					break
				}
				l = next
			}
			rep, err := Call(Request{Op: "git_push", Remote: url, Pushes: pushes, Opts: opts}, nil)
			for _, p := range pushes {
				res := "error conductor broker unreachable"
				if err == nil {
					res = rep.Results[p.Dst]
					if res == "" {
						res = "error " + firstNonEmpty(rep.Refused, rep.Error, "no result")
					}
				}
				if res == "ok" {
					fmt.Fprintf(w, "ok %s\n", p.Dst)
				} else {
					why := strings.TrimPrefix(res, "error ")
					fmt.Fprintf(w, "error %s %s\n", p.Dst, why)
					fmt.Fprintf(os.Stderr, "conductor: push %s refused: %s\n", p.Dst, why)
				}
			}
			fmt.Fprint(w, "\n")
			flush()
		default:
			fmt.Fprintf(os.Stderr, "git-remote-conductor: unsupported command %q\n", line)
			return 1
		}
	}
}

// parsePushSpec reads one `push [+]<src>:<dst>` line, resolving src to the
// local commit (in the jail's own repository).
func parsePushSpec(spec string) GitPush {
	p := GitPush{}
	if strings.HasPrefix(spec, "+") {
		p.Force = true
		spec = spec[1:]
	}
	src, dst, _ := strings.Cut(spec, ":")
	p.Src, p.Dst = src, dst
	if src != "" {
		if out, err := exec.Command("git", "rev-parse", "--verify", "--quiet", src+"^{commit}").Output(); err == nil {
			p.SHA = strings.TrimSpace(string(out))
		} else {
			p.SHA = "unresolved"
		}
	}
	return p
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
