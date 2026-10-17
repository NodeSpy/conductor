//go:build !unix

package browser

import "os"

// Non-unix fallback: os.Root blocks escaping the workspace, but not the
// intra-workspace carve-out redirection the unix build closes with O_NOFOLLOW.
// The agent sandbox (and thus the browser) is supported only on Linux and
// macOS, both unix; this keeps the package compiling elsewhere.

func openWorkspaceFile(ws, rel string, flag int, perm os.FileMode) (*os.File, error) {
	root, err := os.OpenRoot(ws)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(rel, flag, perm)
	if err != nil {
		return nil, unwrapRoot(err)
	}
	return f, nil
}

func mkdirAllWorkspace(ws, relDir string) error {
	if relDir == "." || relDir == "" {
		return nil
	}
	root, err := os.OpenRoot(ws)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll(relDir, 0o755); err != nil {
		return unwrapRoot(err)
	}
	return nil
}

func removeWorkspaceFile(ws, rel string) error {
	root, err := os.OpenRoot(ws)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Remove(rel)
}
