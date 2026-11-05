package flow

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/connector"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// gitLandFixture stands in for the git-land connector plugin, for the
// pr-autopilot pack's moved-head steps: keep, replay and push, done with
// real git in the connection's dir as the plugin documents them (the
// plugin's own tests drive the real one). keep fetches a revision out of a
// workspace under a ref; replay re-creates base..head onto a remote branch's
// head with git merge-tree; push is a plain (never forced) push.
type gitLandFixture struct{}

func init() { connector.RegisterInProcessConnector(gitLandFixture{}) }

func (gitLandFixture) Describe() sdk.Decl {
	str := sdk.Field{Type: "string"}
	return sdk.Decl{
		Kind: sdk.KindConnector, Type: "git-land", Desc: "git-land fixture",
		Connection: sdk.Schema{"dir": str},
		Verbs: []sdk.Verb{
			{Name: "keep", Options: sdk.Schema{"from": str, "rev": str, "ref": str},
				Outputs: sdk.Schema{"sha": str, "ref": str, "exit_code": {Type: "integer"}}},
			{Name: "replay", Options: sdk.Schema{"head": str, "base": str, "onto": str, "remote": str, "branch": str},
				Outputs: sdk.Schema{"onto": str, "moved": {Type: "boolean"}, "clean": {Type: "boolean"}, "head": str, "base": str,
					"commits": {Type: "integer"}, "dropped": {Type: "integer"}, "conflicts": {Type: "array"}, "conflict_commit": str, "reason": str, "exit_code": {Type: "integer"}}},
			{Name: "push", Options: sdk.Schema{"remote": str, "rev": str, "branch": str},
				Outputs: sdk.Schema{"pushed": {Type: "boolean"}, "rejected": {Type: "boolean"}, "stdout": str, "stderr": str, "exit_code": {Type: "integer"}}},
		},
	}
}

func (gitLandFixture) Invoke(req sdk.InvokeRequest) (sdk.InvokeResult, error) {
	dir, _ := req.Connection["dir"].(string)
	o := req.Options
	opt := func(k string) string { s, _ := o[k].(string); return s }
	git := func(d string, args ...string) (string, int) {
		c := exec.Command("git", append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "protocol.file.allow=always"}, args...)...)
		c.Dir = d
		out, err := c.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			code = -1
		}
		return strings.TrimSpace(string(out)), code
	}
	fail := func(what, out string) (sdk.InvokeResult, error) {
		return sdk.InvokeResult{}, sdk.Fail(sdk.CodeInvalid, req.Verb+": "+what+": "+out, nil)
	}
	switch req.Verb {
	case "keep":
		if _, err := os.Stat(dir); err != nil {
			if out, c := git(filepath.Dir(dir), "init", "-q", "--bare", dir); c != 0 {
				return fail("init", out)
			}
		}
		rev := opt("rev")
		if rev == "" {
			rev = "HEAD"
		}
		out, c := git(dir, "fetch", "-q", "--no-tags", "--end-of-options", opt("from"), "+"+rev+":"+opt("ref"))
		if c != 0 {
			return sdk.InvokeResult{Outputs: map[string]any{"exit_code": c, "stderr": out}}, nil
		}
		sha, _ := git(dir, "rev-parse", opt("ref"))
		return sdk.InvokeResult{Outputs: map[string]any{"sha": sha, "ref": opt("ref"), "exit_code": 0}}, nil
	case "replay":
		tracking := "refs/replay/" + opt("branch")
		if out, c := git(dir, "fetch", "-q", "--no-tags", "--end-of-options", opt("remote"), "+refs/heads/"+opt("branch")+":"+tracking); c != 0 {
			return fail("fetch", out)
		}
		onto, _ := git(dir, "rev-parse", tracking)
		_, ff := git(dir, "merge-base", "--is-ancestor", onto, opt("head"))
		res := map[string]any{"onto": onto, "moved": ff != 0, "clean": false, "commits": 0, "conflicts": []any{}, "exit_code": 0}
		if ff == 0 {
			return sdk.InvokeResult{Outputs: res}, nil
		}
		base, c := git(dir, "rev-parse", opt("base")+"^{commit}")
		if opt("base") == "" {
			base, c = git(dir, "merge-base", onto, opt("head"))
		}
		if c != 0 {
			return fail("base", base)
		}
		res["base"] = base
		list, _ := git(dir, "rev-list", "--reverse", base+".."+opt("head"))
		cur, n := onto, 0
		for _, c := range strings.Fields(list) {
			mt, code := git(dir, "merge-tree", "--write-tree", "--name-only", "--no-messages", "--merge-base="+c+"^", cur, c)
			lines := strings.Split(mt, "\n")
			if code == 1 {
				files := []any{}
				for _, f := range lines[1:] {
					files = append(files, f)
				}
				res["conflicts"], res["conflict_commit"] = files, c
				return sdk.InvokeResult{Outputs: res}, nil
			}
			if curTree, _ := git(dir, "rev-parse", cur+"^{tree}"); lines[0] == curTree {
				continue
			}
			msg, _ := git(dir, "log", "-1", "--format=%B", c)
			next, code := git(dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit-tree", lines[0], "-p", cur, "-m", msg)
			if code != 0 {
				return fail("commit-tree", next)
			}
			cur, n = next, n+1
		}
		res["clean"], res["head"], res["commits"] = true, cur, n
		return sdk.InvokeResult{Outputs: res}, nil
	case "push":
		out, c := git(dir, "push", "--end-of-options", opt("remote"), opt("rev")+":refs/heads/"+opt("branch"))
		return sdk.InvokeResult{Outputs: map[string]any{"pushed": c == 0, "rejected": c != 0, "stdout": "", "stderr": out, "exit_code": c}}, nil
	}
	return fail("unknown verb", req.Verb)
}

// gitFixture runs git in dir for a test, failing it on an error.
func gitFixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "init.defaultBranch=main"}, args...)...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
