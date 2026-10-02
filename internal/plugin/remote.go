package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
)

// Remote plugin fetch (#59): a plugin is a binary, published per platform on
// the release's dist refs (gitdist.go) and fetched over plain git from any
// host. A remote source names a repo (and, for a monorepo, a component that
// prefixes its release tags and names its binary); a version: constraint
// selects the highest matching tag; this platform's binary is fetched,
// checksum-verified, and cached, then the existing verify-before-execute +
// sandbox path (#54) takes over.

// RemoteSource is a parsed remote plugin source.
type RemoteSource struct {
	// URL is the repository's git URL (https://, ssh://, file://, or
	// git@host:path).
	URL string
	// Component is "" for a single-plugin repo; else the path inside it
	// ("connectors/sentry").
	Component string
}

// Display is the source as an operator reads it in a message.
func (rs RemoteSource) Display() string {
	if rs.Component == "" {
		return rs.URL
	}
	return rs.URL + "//" + rs.Component
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

// ReleaseAPI lists a source's release tags and fetches one file (the
// binary, or checksums.txt) of a release for this platform. GitDist is the
// implementation; tests inject a stub.
type ReleaseAPI interface {
	ListTags(rs RemoteSource) ([]string, error)
	Download(rs RemoteSource, tag, file, destDir string) (path string, err error)
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
	tags, err := api.ListTags(rs)
	if err != nil {
		return "", "", "", false, fmt.Errorf("list releases for %s: %w", rs.Display(), err)
	}
	tag, ok := config.BestMatch(tags, rs.tagPrefix(), constraint)
	if !ok {
		return "", "", "", false, fmt.Errorf("no release tag satisfies version %q for %s (looked for %q<semver> among %d tags)", constraint, rs.Display(), rs.tagPrefix(), len(tags))
	}
	tmp, err := os.MkdirTemp("", "conductor-plugin-dl-*")
	if err != nil {
		return "", "", "", false, err
	}
	defer os.RemoveAll(tmp)

	asset := rs.AssetName()
	dl, err := api.Download(rs, tag, asset, tmp)
	if err != nil {
		return "", "", "", false, fmt.Errorf("fetch %s from %s %s: %w", asset, rs.Display(), tag, err)
	}
	got, err := fileSha256(dl)
	if err != nil {
		return "", "", "", false, err
	}
	// checksums.txt, when present, is authoritative for what the release published.
	if cs, cerr := api.Download(rs, tag, "checksums.txt", tmp); cerr == nil {
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

// VerifyChecksum checks the file at bin against asset's entry in the
// checksums file at sums. A missing entry is an error: what is verified is
// that the release published exactly these bytes.
func VerifyChecksum(bin, sums, asset string) error {
	want, ok := checksumFor(sums, asset)
	if !ok {
		return fmt.Errorf("checksums.txt does not list %s — refusing an unverified binary", asset)
	}
	got, err := fileSha256(bin)
	if err != nil {
		return err
	}
	if !strings.EqualFold(want, got) {
		return fmt.Errorf("checksum mismatch for %s: release lists %s, fetched %s", asset, want, got)
	}
	return nil
}
