package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
)

// BackupSuffix names the pre-migration copy written next to each file.
const BackupSuffix = ".pre-connectors"

// AutoMigrate transforms the config at mainPath (and every imported file,
// recursively) in place, fail-safe:
//
//   - each file is transformed independently; a file with no legacy
//     constructs is untouched (idempotent — a second run is a no-op).
//   - before a file is replaced, its original is backed up alongside it
//     (<file>.pre-connectors, first backup wins).
//   - after each swap, validate() re-loads the WHOLE config; because both
//     schemas coexist, every intermediate state is loadable — so a validation
//     failure restores just the offending file and stops, leaving a running
//     (partially-migrated, fully-valid) config.
//
// Returns how many files were migrated and the accumulated summary. A non-nil
// error means the config needs manual migration — the caller keeps running on
// the current (restored) state and notifies.
func AutoMigrate(mainPath string, validate func() error, logf func(string, ...any)) (int, []string, error) {
	files, err := discoverFiles(mainPath)
	if err != nil {
		return 0, nil, err
	}
	// Profile behavior is INLINED at each referencing step now, so the whole
	// import tree has to be read before any single file is rewritten: the
	// `agents:` block and the triggers that named it are routinely in
	// different files.
	profiles := map[string]*yaml.Node{}
	var runtimeNames []string
	defaultRuntime := ""
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return 0, nil, fmt.Errorf("read %s: %w", f, err)
		}
		for name, frag := range CollectProfiles(raw) {
			if _, dup := profiles[name]; !dup {
				profiles[name] = frag
			}
		}
		names, def := CollectRuntimes(raw)
		runtimeNames = append(runtimeNames, names...)
		if def != "" && defaultRuntime == "" {
			defaultRuntime = def
		}
	}

	// Transform EVERY file first, then validate the tree ONCE, then commit.
	//
	// Validating after each individual write made the outcome depend on
	// filename order: a file that REFERENCES an `agents:` profile sorts
	// before the file that DEFINES it, so the mid-pass reload saw a tree
	// that was half-migrated — an un-migrated `agents:` block against a
	// schema that no longer has one — and aborted. The box then restored
	// and never migrated, on every boot, forever.
	//
	// The fail-safe is unchanged in kind and stronger in reach: nothing is
	// committed until the whole tree validates, and a failure restores
	// every file, not just the one in hand.
	type pending struct {
		path, backup string
		original     []byte
		output       []byte
		mode         os.FileMode
		summary      []string
	}
	var todo []pending
	inlined := map[string]bool{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return 0, nil, fmt.Errorf("read %s: %w", f, err)
		}
		res, err := TransformWith(raw, profiles, runtimeNames, defaultRuntime)
		if err != nil {
			return 0, nil, fmt.Errorf("config %s needs manual migration: %w", f, err)
		}
		if !res.Changed {
			continue
		}
		for _, n := range res.InlinedProfiles {
			inlined[n] = true
		}
		todo = append(todo, pending{
			path: f, backup: f + BackupSuffix, original: raw,
			output: res.Output, mode: fileMode(f), summary: res.Summary,
		})
	}
	// Only NOW can an unreferenced profile be named. A per-file pass sees
	// one file, where "nothing referenced it" is routinely false — the
	// referencing trigger is in another file of the same tree.
	var orphans []string
	for name := range profiles {
		if !inlined[name] {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans)
	if len(todo) == 0 {
		return 0, nil, nil
	}

	// Back up, then write every transformed file.
	restoreAll := func() error {
		var firstErr error
		for _, p := range todo {
			if err := os.WriteFile(p.path, p.original, p.mode); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("restoring %s: %w", p.path, err)
			}
		}
		return firstErr
	}
	for _, p := range todo {
		if _, err := os.Stat(p.backup); os.IsNotExist(err) {
			if err := os.WriteFile(p.backup, p.original, p.mode); err != nil {
				return 0, nil, fmt.Errorf("write backup %s: %w", p.backup, err)
			}
			// WriteFile's mode is clamped by the umask; a secretful config
			// must keep its exact permissions.
			_ = os.Chmod(p.backup, p.mode)
		}
		tmp := p.path + ".tmp"
		if err := os.WriteFile(tmp, p.output, p.mode); err != nil {
			_ = restoreAll()
			return 0, nil, err
		}
		_ = os.Chmod(tmp, p.mode)
		if err := os.Rename(tmp, p.path); err != nil {
			_ = restoreAll()
			return 0, nil, err
		}
	}

	// One validation, of the whole migrated tree.
	if err := validate(); err != nil {
		if werr := restoreAll(); werr != nil {
			return 0, nil, fmt.Errorf("validation failed (%v) AND restoring failed (%v) — restore from the %s files by hand", err, werr, BackupSuffix)
		}
		return 0, nil, fmt.Errorf("config needs manual migration: the transformed tree did not validate: %w (originals restored; backups alongside as %s)", err, BackupSuffix)
	}

	migrated := len(todo)
	var all []string
	for _, name := range orphans {
		all = append(all, fmt.Sprintf(
			"agents.%s dropped — no step in this config referenced it, and there is no top-level steps: section left to park it in. Its behavior is in the %s backup if you still want it", name, BackupSuffix))
	}
	for _, p := range todo {
		all = append(all, fmt.Sprintf("migrated %s (backup: %s)", p.path, p.backup))
		all = append(all, p.summary...)
		if logf != nil {
			logf("config migrate: %s → connectors schema (backup %s)", p.path, p.backup)
		}
	}
	return migrated, all, nil
}

// fileMode returns the file's current mode (0600 fallback), so a migrated
// secretful config keeps its permissions.
func fileMode(path string) os.FileMode {
	if fi, err := os.Stat(path); err == nil {
		return fi.Mode().Perm()
	}
	return 0o600
}

// discoverFiles returns mainPath plus every file reachable via imports:
// (globs resolved relative to each file's directory), depth-first, deduped.
// Imports run BEFORE env expansion here; an import path containing ${…} is a
// hard error (the transform cannot resolve it without embedding the value).
func discoverFiles(mainPath string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	var walk func(p string) error
	walk = func(p string) error {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		if seen[abs] {
			return nil
		}
		seen[abs] = true
		out = append(out, p)
		raw, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		var probe struct {
			Imports []string `yaml:"imports"`
		}
		// Mask ${VAR} references first — the raw file may not parse without
		// expansion (see Transform).
		if err := yaml.Unmarshal(maskEnv(raw), &probe); err != nil {
			return fmt.Errorf("parse %s: %w", p, err)
		}
		dir := filepath.Dir(p)
		for _, imp := range probe.Imports {
			// The probe parsed masked bytes, so restore any ${VAR} tokens —
			// otherwise the env-ref guard below can never fire and a masked
			// path would silently glob to nothing (skipping the file).
			imp = string(unmaskEnv([]byte(imp)))
			if containsEnvRef(imp) {
				return fmt.Errorf("%s: import %q references an environment variable — resolve it by hand before migrating", p, imp)
			}
			// The loader's own resolution (config.GlobImport): ** is refused
			// by name here too — silently under-collecting and then having
			// the strict loader refuse the same pattern wedged the daemon
			// degraded.
			matches, err := config.GlobImport(dir, p, imp)
			if err != nil {
				return err
			}
			for _, m := range matches {
				if err := walk(m); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(mainPath); err != nil {
		return nil, err
	}
	return out, nil
}

func containsEnvRef(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '$' && s[i+1] == '{' {
			return true
		}
	}
	return false
}
