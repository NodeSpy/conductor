package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// packStagePrefix names the staging roots StagePacks creates under the
// config's .conductor/ dir — on the same filesystem as the live vendor dir,
// so Commit's swap is a rename, not a copy.
const packStagePrefix = ".packs-stage-"

// staleStageAge is how old an abandoned staging root (a daemon killed
// mid-refresh) must be before StagePacks sweeps it. Far longer than any one
// resolution takes, so a concurrent stage is never swept out from under its
// owner.
const staleStageAge = time.Hour

// PackStage is a pack resolution held OFF to the side of the live vendor dir
// and lockfile. Auto-update resolves into one, loads the config against it,
// and only then commits: a pack update the config cannot load (a newer
// release requiring a connector this box does not have) is discarded with
// nothing live touched, so the next restart boots the last state that loaded
// rather than coming up degraded on an update that was never applied.
type PackStage struct {
	configPath string
	root       string
	// Lock is the staged lockfile.
	Lock *Lockfile
}

// StagePacks resolves the config's packs into a fresh staging root next to
// the live vendor dir. The live tree and lockfile are only read (for
// `hold:`), never written. Discard the stage when done with it; Commit swaps
// it in.
func StagePacks(configPath string) (*PackStage, error) {
	parent := filepath.Join(filepath.Dir(configPath), ".conductor")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	sweepStaleStages(parent)
	root, err := os.MkdirTemp(parent, packStagePrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("stage pack update: %w", err)
	}
	lock, err := resolvePacksInto(configPath, root, false)
	if err != nil {
		os.RemoveAll(root)
		return nil, err
	}
	return &PackStage{configPath: configPath, root: root, Lock: lock}, nil
}

// sweepStaleStages removes staging roots abandoned by a process that died
// before discarding them. Best effort: a leftover is harmless clutter.
func sweepStaleStages(parent string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), packStagePrefix) {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > staleStageAge {
			os.RemoveAll(filepath.Join(parent, e.Name()))
		}
	}
}

// Load loads the config with its packs read from the stage — exactly what
// the daemon would load after Commit and a restart.
func (s *PackStage) Load() (*Config, error) { return load(s.configPath, s.root) }

// Commit swaps the staged vendor tree and lockfile in for the live ones and
// discards the stage. A stage that wrote no lockfile (an empty `packs:`
// block) has nothing to swap. On failure the live tree is put back as it was.
func (s *PackStage) Commit() error {
	defer s.Discard()
	stagedLock := filepath.Join(s.root, LockfileName)
	if _, err := os.Stat(stagedLock); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	configDir := filepath.Dir(s.configPath)
	live := packVendorDir(configDir)
	staged := packVendorDir(s.root)
	// The previous live tree parks inside the stage root, so Discard (above)
	// removes it once the swap is done.
	prev := filepath.Join(s.root, "previous")
	hadLive := true
	if err := os.Rename(live, prev); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("commit pack update: move aside %s: %w", live, err)
		}
		hadLive = false
	}
	restoreLive := func() {
		if hadLive {
			os.Rename(prev, live)
		}
	}
	if err := os.Rename(staged, live); err != nil {
		restoreLive()
		return fmt.Errorf("commit pack update: swap in %s: %w", live, err)
	}
	if err := os.Rename(stagedLock, filepath.Join(configDir, LockfileName)); err != nil {
		os.Rename(live, staged)
		restoreLive()
		return fmt.Errorf("commit pack update: write %s: %w", LockfileName, err)
	}
	return nil
}

// Discard removes the stage. Safe to call more than once, and after Commit.
func (s *PackStage) Discard() { os.RemoveAll(s.root) }
