package gitpush

import (
	"fmt"
	"sort"
	"strings"
)

// gitFn runs conductor's hardened git in the push's trusted dir.
type gitFn func(stdin []byte, args ...string) (string, error)

// notOnRemote lists head's commits that no remote tip reaches, oldest
// first: what a push of a new branch must sign. tips are the remote's
// branch and tag tips (ls-remote); one this repository lacks cannot exclude
// anything, so when the local remote-tracking refs claim more is pushed than
// the remote's own tips prove, the missing tips are fetched (into the push's
// scratch object store) and the walk is redone. The local claim itself is
// never trusted.
func notOnRemote(git gitFn, head string, tips map[string]string, remote string) (string, error) {
	walk := func() (string, error) {
		var in strings.Builder
		in.WriteString(head + "\n")
		for sha := range tips {
			in.WriteString("^" + sha + "\n")
		}
		return git([]byte(in.String()), "rev-list", "--reverse", "--topo-order", "--ignore-missing", "--stdin")
	}
	out, err := walk()
	if err != nil {
		return "", err
	}
	claimed, err := git(nil, "rev-list", head, "--not", "--remotes")
	if err != nil {
		return out, nil // no local claim to compare with: sign what the remote cannot prove
	}
	local := map[string]bool{}
	for _, c := range strings.Fields(claimed) {
		local[c] = true
	}
	stale := false
	for _, c := range strings.Fields(out) {
		if !local[c] {
			stale = true
			break
		}
	}
	if !stale {
		return out, nil
	}
	var missing []string
	for sha, ref := range tips {
		if _, err := git(nil, "cat-file", "-e", sha+"^{commit}"); err != nil && strings.HasPrefix(ref, "refs/") {
			missing = append(missing, ref)
		}
	}
	sort.Strings(missing)
	for i := 0; i < len(missing); i += 64 {
		end := min(i+64, len(missing))
		args := append([]string{"fetch", "--no-write-fetch-head", "--no-tags", "--no-recurse-submodules", "--end-of-options", remote}, missing[i:end]...)
		if _, err := git(nil, args...); err != nil {
			return "", fmt.Errorf("fetch the remote's tips to learn what is already pushed: %w", err)
		}
	}
	return walk()
}

// ciPaths are CI definitions forges run on a push — with the repository's
// secrets. Matched as a path prefix (a dir, trailing /) or exactly.
var ciPaths = []string{
	".github/workflows/", ".github/actions/", ".gitea/workflows/", ".forgejo/workflows/",
	".gitlab-ci.yml", ".gitlab-ci.yaml", ".gitlab/ci/", ".circleci/", ".buildkite/",
	"Jenkinsfile", "azure-pipelines.yml", "azure-pipelines.yaml", ".azure-pipelines/",
	".travis.yml", "bitbucket-pipelines.yml", ".drone.yml", ".drone.yaml",
	".woodpecker.yml", ".woodpecker.yaml", ".woodpecker/", "appveyor.yml", ".appveyor.yml",
}

// IsCIPath reports a repository path that is a CI definition.
func IsCIPath(p string) bool {
	for _, c := range ciPaths {
		if strings.HasSuffix(c, "/") {
			if strings.HasPrefix(p, c) {
				return true
			}
		} else if p == c {
			return true
		}
	}
	return false
}

// ErrCIChange is a push refused for touching a CI definition.
var ErrCIChange = fmt.Errorf("gitpush: push touches CI config; refused by sandbox.push.deny_ci_changes")

// checkCIChanges refuses commits whose own change (against their parent;
// for a merge, what differs from every parent — never what a merged branch
// already carried) touches a CI definition.
func checkCIChanges(git gitFn, commits []string) error {
	out, err := git([]byte(strings.Join(commits, "\n")+"\n"), "diff-tree", "--stdin", "-r", "-c", "--root", "--name-only", "--no-renames", "-z")
	if err != nil {
		return fmt.Errorf("gitpush: list the paths the push changes: %w", err)
	}
	// -z output: "<commit>\0path\0path\0<commit>\0…"; a commit line is a
	// bare object id.
	commit := ""
	for _, f := range strings.Split(out, "\x00") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if isSHA(f) {
			commit = f
			continue
		}
		if IsCIPath(f) {
			short := commit
			if len(short) > 12 {
				short = short[:12]
			}
			return fmt.Errorf("%w (commit %s changes %s)", ErrCIChange, short, f)
		}
	}
	return nil
}
