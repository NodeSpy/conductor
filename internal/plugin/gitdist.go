package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
)

// Git distribution (plugin-contract.md X2): plugins and conductor itself are
// fetched over plain git from any host — no forge CLI, no forge API. A
// release is a tag; its release workflow ALSO publishes each platform's
// binary as its own commit on
//
//	refs/dist/<tag>/<goos>_<goarch>
//
// whose tree holds that one binary (named AssetName) and checksums.txt. One
// ref per platform means a fetch moves exactly the bytes this machine runs,
// with an ordinary shallow fetch. Ordinary clones never fetch refs/dist/*, so
// source checkouts stay small. Private repositories work with whatever git
// credentials the daemon's user has (a deploy key, a credential helper).

// DistRefPrefix is the namespace release binaries are published under.
const DistRefPrefix = "refs/dist/"

// Platform is this machine's dist ref suffix.
func Platform() string { return runtime.GOOS + "_" + runtime.GOARCH }

// DistRef is the ref carrying tag's binary for platform.
func DistRef(tag, platform string) string { return DistRefPrefix + tag + "/" + platform }

// GitDist implements ReleaseAPI over git.
type GitDist struct {
	// Git runs one git command returning its combined output, and GitOut
	// one returning stdout alone (object reads). nil uses config.RunGit /
	// config.RunGitStdout.
	Git    func(dir string, args ...string) (string, error)
	GitOut func(dir string, args ...string) ([]byte, error)
}

func (g GitDist) git(dir string, args ...string) (string, error) {
	if g.Git != nil {
		return g.Git(dir, args...)
	}
	return config.RunGit(dir, args...)
}

func (g GitDist) gitOut(dir string, args ...string) ([]byte, error) {
	if g.GitOut != nil {
		return g.GitOut(dir, args...)
	}
	return config.RunGitStdout(dir, args...)
}

func checkURL(url string) error {
	if strings.HasPrefix(url, "-") || !config.SafeGitTransport(url) {
		return fmt.Errorf("git source %q: unsupported transport — use https://, ssh://, file://, or git@host:path", url)
	}
	return nil
}

// ListTags lists the tags with a binary published for THIS platform.
func (g GitDist) ListTags(rs RemoteSource) ([]string, error) {
	if err := checkURL(rs.URL); err != nil {
		return nil, err
	}
	out, err := g.git("", "ls-remote", "--refs", rs.URL, DistRefPrefix+rs.tagPrefix()+"*")
	if err != nil {
		return nil, fmt.Errorf("git ls-remote %s: %s", rs.Display(), gitDetail(out, err))
	}
	return distTags(out, Platform()), nil
}

// distTags reads `<sha>\trefs/dist/<tag>/<platform>` lines into the tags
// published for platform.
func distTags(lsRemote, platform string) []string {
	seen := map[string]bool{}
	var tags []string
	for _, line := range strings.Split(lsRemote, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || !strings.HasPrefix(f[1], DistRefPrefix) {
			continue
		}
		rest := strings.TrimPrefix(f[1], DistRefPrefix)
		i := strings.LastIndex(rest, "/")
		if i <= 0 || rest[i+1:] != platform {
			continue
		}
		if tag := rest[:i]; !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	return tags
}

// Download fetches tag's commit for this platform and writes file (the
// binary, or checksums.txt) from it into destDir. Each call uses its own
// throwaway repository, so concurrent fetches never share FETCH_HEAD.
func (g GitDist) Download(rs RemoteSource, tag, file, destDir string) (string, error) {
	if err := checkURL(rs.URL); err != nil {
		return "", err
	}
	if strings.HasPrefix(tag, "-") || strings.ContainsAny(file, "/\\") || strings.HasPrefix(file, "-") {
		return "", fmt.Errorf("git dist: refusing tag %q / file %q", tag, file)
	}
	repo, err := os.MkdirTemp(destDir, ".dist-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(repo)
	if out, err := g.git(repo, "init", "-q"); err != nil {
		return "", fmt.Errorf("git init: %s", gitDetail(out, err))
	}
	ref := DistRef(tag, Platform())
	if out, err := g.git(repo, "fetch", "-q", "--depth", "1", "--no-tags", rs.URL, ref); err != nil {
		return "", fmt.Errorf("git fetch %s %s: %s", rs.Display(), ref, gitDetail(out, err))
	}
	blob, err := g.gitOut(repo, "cat-file", "blob", "FETCH_HEAD:"+file)
	if err != nil {
		return "", fmt.Errorf("%s has no %s at %s: %v", rs.Display(), file, ref, err)
	}
	dest := filepath.Join(destDir, file)
	if err := os.WriteFile(dest, blob, 0o600); err != nil {
		return "", err
	}
	return dest, nil
}

func gitDetail(out string, err error) string {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "fatal:") || strings.HasPrefix(l, "error:") {
			return l
		}
	}
	if s := strings.TrimSpace(out); s != "" && len(s) < 300 {
		return s
	}
	return err.Error()
}
