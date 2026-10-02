package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
)

// Remote plugin fetch (#59): a plugin is a binary, so — unlike a pack, which is
// a git tree — it is served as a GitHub release ASSET. A remote source names a
// repo (and, for a monorepo, a component that prefixes its release tags and
// asset names); a version: constraint selects the highest matching tag; the
// per-platform binary is downloaded, checksum-verified, and cached, then the
// existing verify-before-execute + sandbox path (#54) takes over.

// RemoteSource is a parsed remote plugin source.
type RemoteSource struct {
	Repo      string // "owner/name"
	Component string // "" for a single-plugin repo; else the //subdir
}

// ParseRemoteSource recognizes github.com/<owner>/<repo>[//<component>] and
// https:// variants. ok=false for a local filesystem path.
func ParseRemoteSource(src string) (RemoteSource, bool) {
	s := strings.TrimSpace(src)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if !strings.HasPrefix(s, "github.com/") {
		return RemoteSource{}, false
	}
	s = strings.TrimPrefix(s, "github.com/")
	var comp string
	if i := strings.Index(s, "//"); i >= 0 {
		comp = strings.Trim(s[i+2:], "/")
		s = s[:i]
	}
	parts := strings.SplitN(strings.Trim(s, "/"), "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return RemoteSource{}, false
	}
	return RemoteSource{Repo: parts[0] + "/" + parts[1], Component: comp}, true
}

// AssetName is the per-platform binary conductor expects in a release. The
// component may be a PATH inside the repo ("connectors/sentry"), but an asset
// name is flat — so the LEAF names the asset: connectors/sentry publishes
// conductor-sentry_linux_amd64.
func (rs RemoteSource) AssetName() string {
	base := rs.Component
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if base == "" {
		base = "plugin"
	}
	return fmt.Sprintf("conductor-%s_%s_%s", base, runtime.GOOS, runtime.GOARCH)
}

// tagPrefix is the component prefix on a monorepo's release tags ("sentry/").
func (rs RemoteSource) tagPrefix() string {
	if rs.Component == "" {
		return ""
	}
	return rs.Component + "/"
}

// ReleaseAPI lists a repo's release tags and downloads an asset. Injectable so
// resolution + verification are testable without the gh CLI / network.
type ReleaseAPI interface {
	ListTags(repo string) ([]string, error)
	Download(repo, tag, asset, destDir string) (path string, err error)
}

// FetchRemote resolves the constraint to a release tag, downloads the
// per-platform binary + checksums, verifies the sha (checksums.txt and/or the
// pinned sha256), and caches it. Returns the cached path, the resolved tag, and
// the verified sha.
func FetchRemote(rs RemoteSource, constraint, pinnedSha, cacheDir string, api ReleaseAPI) (binPath, tag, sha string, err error) {
	binPath, tag, sha, _, err = FetchRemoteVerified(rs, constraint, pinnedSha, cacheDir, api)
	return binPath, tag, sha, err
}

// FetchRemoteVerified is FetchRemote that also reports whether the download
// was VERIFIED against the release: its sha matched the release's own
// checksums.txt entry for the asset (or a config-pinned sha). A release that
// publishes no checksums.txt, or one that does not list the asset, still
// installs — the sha is recorded and checked before every exec — but it is
// not verified, and nothing that is granted on the strength of a verified
// release (an official source's default event trust) is granted to it.
func FetchRemoteVerified(rs RemoteSource, constraint, pinnedSha, cacheDir string, api ReleaseAPI) (binPath, tag, sha string, verified bool, err error) {
	tags, err := api.ListTags(rs.Repo)
	if err != nil {
		return "", "", "", false, fmt.Errorf("list releases for %s: %w", rs.Repo, err)
	}
	tag, ok := config.BestMatch(tags, rs.tagPrefix(), constraint)
	if !ok {
		return "", "", "", false, fmt.Errorf("no release tag satisfies version %q for %s (looked for %q<semver> among %d tags)", constraint, rs.Repo, rs.tagPrefix(), len(tags))
	}
	tmp, err := os.MkdirTemp("", "conductor-plugin-dl-*")
	if err != nil {
		return "", "", "", false, err
	}
	defer os.RemoveAll(tmp)

	asset := rs.AssetName()
	dl, err := api.Download(rs.Repo, tag, asset, tmp)
	if err != nil {
		return "", "", "", false, fmt.Errorf("download %s from %s %s: %w", asset, rs.Repo, tag, err)
	}
	got, err := fileSha256(dl)
	if err != nil {
		return "", "", "", false, err
	}
	// checksums.txt, when present, is authoritative for what the release published.
	if cs, cerr := api.Download(rs.Repo, tag, "checksums.txt", tmp); cerr == nil {
		want, found := checksumFor(cs, asset)
		if found && !strings.EqualFold(want, got) {
			return "", "", "", false, fmt.Errorf("checksum mismatch for %s@%s: release lists %s, downloaded %s", asset, tag, want, got)
		}
		verified = found
	}
	// A config-pinned sha256 must also match (defense in depth).
	if pinnedSha != "" {
		if !strings.EqualFold(strings.TrimSpace(pinnedSha), got) {
			return "", "", "", false, fmt.Errorf("sha256 pin mismatch for %s@%s: config pins %s, downloaded %s", asset, tag, pinnedSha, got)
		}
		verified = true
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", "", "", false, err
	}
	dest := filepath.Join(cacheDir, asset)
	if err := copyExecutable(dl, dest); err != nil {
		return "", "", "", false, err
	}
	return dest, tag, got, verified, nil
}

func fileSha256(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// checksumFor finds asset's sha in a `<sha>  <name>` checksums file.
func checksumFor(path, asset string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && filepath.Base(f[len(f)-1]) == asset {
			return f[0], true
		}
	}
	return "", false
}

// copyExecutable installs src's bytes at dst atomically: it writes a sibling
// temp file and renames it over dst. Rename, unlike an in-place O_TRUNC open,
// replaces dst even while dst is a currently-executing binary — Linux returns
// ETXTBSY ("text file busy") if you open a running executable for writing, but a
// rename just repoints the path (the running process keeps its now-unlinked
// inode). This is what lets an engine plugin be refreshed while it is loaded;
// the old in-place write failed every reconcile for any live engine.
func copyExecutable(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".new-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below has consumed it
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

// GHReleaseAPI implements ReleaseAPI via the gh CLI — conductor's release
// transport, same as self-update.
type GHReleaseAPI struct{}

func (GHReleaseAPI) ListTags(repo string) ([]string, error) {
	out, err := exec.Command("gh", "api", "--paginate", "repos/"+repo+"/releases", "--jq", ".[].tag_name").Output()
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			tags = append(tags, l)
		}
	}
	return tags, nil
}

func (GHReleaseAPI) Download(repo, tag, asset, destDir string) (string, error) {
	cmd := exec.Command("gh", "release", "download", tag, "--repo", repo, "--pattern", asset, "--dir", destDir, "--clobber")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return filepath.Join(destDir, asset), nil
}
