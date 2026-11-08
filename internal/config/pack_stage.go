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
	return stagePacks(configPath, nil)
}

func stagePacks(configPath string, pins map[string]string) (*PackStage, error) {
	parent := filepath.Join(filepath.Dir(configPath), ".conductor")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	sweepStaleStages(parent)
	root, err := os.MkdirTemp(parent, packStagePrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("stage pack update: %w", err)
	}
	lock, err := resolvePacksPinned(configPath, root, false, pins)
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

// PackFallback is one pack StagePacksNewestLoadable could not take at its
// newest release.
type PackFallback struct {
	Instance string
	Newest   string // the newest release its constraint accepts
	Took     string // the release staged instead; "" = held at the current one
	Err      error  // why Newest did not load
}

// StagePacksNewestLoadable is auto-update's fallback when the newest packs,
// staged together, do not load: each top-level pack instead takes the newest
// release within its `version:` constraint that loads, the others held at
// what the lockfile has. It never goes below the current release: a pack
// with no newer loadable release stays where it is. So one pack that cannot
// move never holds another back. Instances are tried in name order, each
// against the choices before it. Returns the stage and every pack left short
// of its newest release.
//
// Each try is a full staged resolution and load, so this is the slow path,
// taken only when the fast one failed.
func StagePacksNewestLoadable(configPath string) (*PackStage, []PackFallback, error) {
	packs, _, err := loadPacksBlock(configPath)
	if err != nil {
		return nil, nil, err
	}
	if err := applyPackSourceDefaults(packs); err != nil {
		return nil, nil, err
	}
	prevLock, _ := ReadLockfile(filepath.Dir(configPath))
	current := map[string]string{}
	if prevLock != nil {
		for _, e := range prevLock.Packs {
			if e.Resolved != "" && e.Resolved != "local" {
				current[e.Instance] = e.Resolved
			}
		}
	}
	// Every pack starts at its current release.
	pins := map[string]string{}
	for name := range packs {
		if c := current[name]; c != "" {
			pins[name] = c
		}
	}
	var held []PackFallback
	for _, name := range sortedPackKeys(packs) {
		inst := packs[name]
		spec, err := parseSource(inst.Source, filepath.Dir(configPath))
		c := strings.TrimSpace(inst.Version)
		if err != nil || !spec.git || spec.ref != "" || inst.Hold || c == "" {
			continue // nothing to choose between: it resolves one way only
		}
		cands, err := versionCandidates(spec, c)
		if err != nil {
			return nil, nil, fmt.Errorf("pack %q: %w", name, err)
		}
		if len(cands) == 0 {
			continue
		}
		fb := PackFallback{Instance: name, Newest: cands[0]}
		for _, tag := range cands {
			trial := map[string]string{}
			for k, v := range pins {
				trial[k] = v
			}
			trial[name] = tag
			st, err := stagePacks(configPath, trial)
			if err != nil {
				if fb.Err == nil {
					fb.Err = err
				}
				continue
			}
			_, lerr := st.Load()
			resolved := ""
			for _, e := range st.Lock.Packs {
				if e.Instance == name {
					resolved = e.Resolved
				}
			}
			st.Discard()
			if resolved != "" && resolved == current[name] {
				break // reached the current release: held there
			}
			if lerr == nil {
				pins[name] = tag
				if tag != cands[0] {
					fb.Took = tag
				}
				break
			}
			if fb.Err == nil {
				fb.Err = lerr
			}
		}
		if fb.Err != nil {
			held = append(held, fb)
		}
	}
	st, err := stagePacks(configPath, pins)
	if err != nil {
		return nil, held, err
	}
	return st, held, nil
}

// PacksNeedResolve reports whether a config's packs: block asks for more
// than its lockfile and vendor dir hold: an instance added or removed, or
// one whose source or version: constraint changed since it was locked. A
// config edit that says yes must have its packs resolved (staged, loaded,
// committed) before the daemon can load it; one that says no loads against
// what is vendored now.
func PacksNeedResolve(configPath string) (bool, error) {
	packs, _, err := loadPacksBlock(configPath)
	if err != nil {
		return false, err
	}
	if err := applyPackSourceDefaults(packs); err != nil {
		return false, err
	}
	dir := filepath.Dir(configPath)
	lock, err := ReadLockfile(dir)
	if err != nil {
		return false, err
	}
	locked := map[string]LockEntry{}
	if lock != nil {
		for _, e := range lock.Packs {
			if !strings.Contains(e.Instance, "/") {
				locked[e.Instance] = e
			}
		}
	}
	if len(locked) != len(packs) {
		return true, nil
	}
	for name, inst := range packs {
		e, ok := locked[name]
		if !ok || e.Source != inst.Source || e.Version != strings.TrimSpace(inst.Version) {
			return true, nil
		}
		if _, err := os.Stat(filepath.Join(packVendorDir(dir), name, PackManifestFile)); err != nil {
			return true, nil
		}
	}
	return false, nil
}

// StagePacksKeepingLocked is StagePacks for a config edit: every instance
// whose source and version: are what the lockfile records stays at its
// locked revision, so adding, changing or removing one pack never moves the
// others onto newer releases. The changed ones resolve fresh.
func StagePacksKeepingLocked(configPath string) (*PackStage, error) {
	packs, _, err := loadPacksBlock(configPath)
	if err != nil {
		return nil, err
	}
	if err := applyPackSourceDefaults(packs); err != nil {
		return nil, err
	}
	pins := map[string]string{}
	if lock, _ := ReadLockfile(filepath.Dir(configPath)); lock != nil {
		for _, e := range lock.Packs {
			inst, ok := packs[e.Instance]
			if ok && e.Resolved != "" && e.Resolved != "local" && e.Source == inst.Source && e.Version == strings.TrimSpace(inst.Version) {
				pins[e.Instance] = e.Resolved
			}
		}
	}
	st, err := stagePacks(configPath, pins)
	if err != nil || len(packs) > 0 {
		return st, err
	}
	// The edit removed the last pack: an empty vendor tree and lockfile
	// swap in on Commit, so the removed copies go with it.
	if err := os.MkdirAll(packVendorDir(st.root), 0o755); err != nil {
		st.Discard()
		return nil, err
	}
	if err := writeLockfile(st.root, st.Lock); err != nil {
		st.Discard()
		return nil, err
	}
	return st, nil
}
