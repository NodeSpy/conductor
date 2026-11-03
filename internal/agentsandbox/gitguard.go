package agentsandbox

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The git guard. Conductor's own git never trusts a workspace's git files
// (it reads the layout recorded at registration and runs hardened), but the
// operator's git does: a .git pointer, commondir or alternates an agent
// rewrote, a core.fsmonitor or filter it added, a hook it planted, runs on
// the operator's machine the next time they run git there. The guard
// records those files when the launch starts and puts them back — or
// quarantines what was added — whenever they change, reporting each.

// guardFile is one recorded git file.
type guardFile struct {
	path   string
	data   []byte
	exists bool
	// config: only its executable settings (dangerousGitKeys) are held to
	// the record; the agent's other changes (user.name) stand.
	config bool
}

type gitGuard struct {
	gitPath string // <ws>/.git
	isDir   bool
	files   []guardFile
	hooks   string
	hookSet map[string]string
}

// newGitGuard records ws's git files (nil: no .git).
func newGitGuard(ws string) *gitGuard {
	gp := filepath.Join(ws, ".git")
	fi, err := os.Lstat(gp)
	if err != nil {
		return nil
	}
	g := &gitGuard{gitPath: gp}
	switch {
	case fi.Mode().IsRegular():
		g.files = append(g.files, recordFile(gp, false))
	case fi.IsDir():
		g.isDir = true
		g.files = append(g.files,
			recordFile(filepath.Join(gp, "config"), true),
			recordFile(filepath.Join(gp, "commondir"), false),
			recordFile(filepath.Join(gp, "objects", "info", "alternates"), false),
		)
		g.hooks = filepath.Join(gp, "hooks")
		g.hookSet = hookState(g.hooks)
	default:
		return nil
	}
	return g
}

func recordFile(p string, config bool) guardFile {
	b := readInHome(p)
	_, err := os.Lstat(p)
	return guardFile{path: p, data: b, exists: err == nil, config: config}
}

// check restores what changed and reports each change.
func (g *gitGuard) check() []string {
	var out []string
	fi, err := os.Lstat(g.gitPath)
	if err != nil || fi.IsDir() != g.isDir || (!g.isDir && !fi.Mode().IsRegular()) {
		// .git itself replaced (a dir by a pointer, a file by a link): the
		// replacement is moved aside; the original cannot be rebuilt here.
		if err == nil {
			q := quarantine(g.gitPath)
			out = append(out, fmt.Sprintf("%s was replaced; the replacement is quarantined at %s", g.gitPath, q))
		} else {
			out = append(out, fmt.Sprintf("%s was removed", g.gitPath))
		}
		if !g.isDir && len(g.files) == 1 && g.files[0].exists {
			if err := writeFileAtomic(g.gitPath, g.files[0].data); err == nil {
				out[len(out)-1] += "; the recorded pointer is restored"
			}
		}
		return out
	}
	for i := range g.files {
		if r := g.files[i].restore(); r != "" {
			out = append(out, r)
		}
	}
	if g.hooks != "" {
		now := hookState(g.hooks)
		var names []string
		for n, st := range now {
			if g.hookSet[n] != st && !strings.HasSuffix(n, ".sample") && strings.HasPrefix(st, "x") {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			p := filepath.Join(g.hooks, n)
			_ = os.Chmod(p, 0o600)
			q := quarantine(p)
			out = append(out, fmt.Sprintf("hook %s was added or changed; quarantined at %s", p, q))
		}
		g.hookSet = hookState(g.hooks)
	}
	return out
}

// restore puts one file back as recorded (a config only when its
// executable settings changed). "" when it is unchanged.
func (f *guardFile) restore() string {
	fi, err := os.Lstat(f.path)
	exists := err == nil
	isFile := exists && fi.Mode().IsRegular()
	var cur []byte
	if isFile {
		cur = readInHome(f.path)
	}
	switch {
	case !f.exists && !exists:
		return ""
	case !f.exists && exists:
		q := quarantine(f.path)
		return fmt.Sprintf("%s was added; quarantined at %s", f.path, q)
	case f.exists && isFile && !f.config && bytes.Equal(cur, f.data):
		return ""
	case f.exists && isFile && f.config:
		before, after := dangerousGitKeys(f.data), dangerousGitKeys(cur)
		if before == after {
			return ""
		}
		if err := writeFileAtomic(f.path, f.data); err != nil {
			return fmt.Sprintf("%s gained executable settings (%s) and could not be restored: %v", f.path, diffKeys(before, after), err)
		}
		return fmt.Sprintf("%s gained executable settings (%s); restored", f.path, diffKeys(before, after))
	}
	if exists && !isFile {
		quarantine(f.path)
	}
	if err := writeFileAtomic(f.path, f.data); err != nil {
		return fmt.Sprintf("%s changed and could not be restored: %v", f.path, err)
	}
	return fmt.Sprintf("%s changed; restored", f.path)
}

// quarantine moves p aside, made non-executable, and returns where.
func quarantine(p string) string {
	q := fmt.Sprintf("%s.conductor-quarantined-%d", p, time.Now().UnixNano())
	if os.Rename(p, q) != nil {
		_ = os.RemoveAll(p)
		return "(removed)"
	}
	if fi, err := os.Lstat(q); err == nil && fi.Mode().IsRegular() {
		_ = os.Chmod(q, 0o600)
	}
	return q
}

// writeFileAtomic replaces p by rename (never through a link at p).
func writeFileAtomic(p string, b []byte) error {
	dir := filepath.Dir(p)
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	tmp, err := os.CreateTemp(dir, ".conductor-restore-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// hookState is each hook's mode and size ("x…" when executable).
func hookState(dir string) map[string]string {
	out := map[string]string{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		fi, err := os.Lstat(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		x := "-"
		if fi.Mode()&0o111 != 0 || fi.Mode()&os.ModeSymlink != 0 {
			x = "x"
		}
		out[e.Name()] = fmt.Sprintf("%s%o:%d:%d", x, fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
	}
	return out
}

// dangerousGitKeys are a git config's settings that make git run a
// program, load more config, or reach a repository somewhere else — as a
// sorted, comparable string.
func dangerousGitKeys(b []byte) string {
	var out []string
	section := ""
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				continue
			}
			h := strings.TrimSpace(line[1:end])
			if i := strings.IndexAny(h, " \t"); i >= 0 {
				sub := strings.Trim(strings.TrimSpace(h[i:]), `"`)
				h = strings.ToLower(h[:i]) + "." + sub
			} else {
				h = strings.ToLower(h)
			}
			section = h
			line = strings.TrimSpace(line[end+1:])
			if line == "" {
				continue
			}
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			v = "true"
		}
		key := section + "." + strings.ToLower(strings.TrimSpace(k))
		if dangerousGitKey(key) {
			out = append(out, key+"="+strings.TrimSpace(v))
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

var dangerousExact = map[string]bool{
	"core.fsmonitor": true, "core.hookspath": true, "core.sshcommand": true, "core.gitproxy": true,
	"core.askpass": true, "core.editor": true, "core.pager": true, "core.worktree": true,
	"core.alternaterefscommand": true, "sequence.editor": true, "diff.external": true,
	"credential.helper": true, "gpg.program": true, "include.path": true, "protocol.allow": true,
	"uploadpack.packobjectshook": true, "receive.procreceiverefs": true, "extensions.worktreeconfig": true,
}

func dangerousGitKey(key string) bool {
	if dangerousExact[key] {
		return true
	}
	parts := strings.Split(key, ".")
	first, last := parts[0], parts[len(parts)-1]
	switch first {
	case "alias", "pager", "includeif":
		return true
	case "filter":
		return last == "clean" || last == "smudge" || last == "process"
	case "diff":
		return last == "textconv" || last == "command"
	case "merge":
		return last == "driver"
	case "difftool", "mergetool":
		return last == "cmd" || last == "path"
	case "gpg", "credential":
		return last == "program" || last == "helper"
	case "protocol":
		return last == "allow"
	case "submodule":
		return last == "update"
	case "remote":
		return last == "uploadpack" || last == "receivepack" || last == "proxy" || last == "vcs"
	}
	return false
}

func diffKeys(before, after string) string {
	had := map[string]bool{}
	for _, k := range strings.Split(before, "\n") {
		had[k] = true
	}
	var added []string
	for _, k := range strings.Split(after, "\n") {
		if k != "" && !had[k] {
			added = append(added, k)
		}
	}
	if len(added) == 0 {
		return "removed"
	}
	return strings.Join(added, ", ")
}
