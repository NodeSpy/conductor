package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
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
	migrated := 0
	var all []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return migrated, all, fmt.Errorf("read %s: %w", f, err)
		}
		res, err := Transform(raw)
		if err != nil {
			return migrated, all, fmt.Errorf("config %s needs manual migration: %w", f, err)
		}
		if !res.Changed {
			continue
		}
		backup := f + BackupSuffix
		if _, err := os.Stat(backup); os.IsNotExist(err) {
			if err := os.WriteFile(backup, raw, fileMode(f)); err != nil {
				return migrated, all, fmt.Errorf("write backup %s: %w", backup, err)
			}
		}
		tmp := f + ".tmp"
		if err := os.WriteFile(tmp, res.Output, fileMode(f)); err != nil {
			return migrated, all, err
		}
		if err := os.Rename(tmp, f); err != nil {
			return migrated, all, err
		}
		if err := validate(); err != nil {
			// Restore the original and stop: the box keeps running on the
			// state that was valid a moment ago.
			if werr := os.WriteFile(f, raw, fileMode(f)); werr != nil {
				return migrated, all, fmt.Errorf("validation failed (%v) AND restoring %s failed (%v) — restore from %s by hand", err, f, werr, backup)
			}
			return migrated, all, fmt.Errorf("config %s needs manual migration: transformed config did not validate: %w (original restored; backup at %s)", f, err, backup)
		}
		migrated++
		all = append(all, fmt.Sprintf("migrated %s (backup: %s)", f, backup))
		all = append(all, res.Summary...)
		if logf != nil {
			logf("config migrate: %s → connectors schema (backup %s)", f, backup)
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
		if err := yaml.Unmarshal(raw, &probe); err != nil {
			return fmt.Errorf("parse %s: %w", p, err)
		}
		dir := filepath.Dir(p)
		for _, imp := range probe.Imports {
			if containsEnvRef(imp) {
				return fmt.Errorf("%s: import %q references an environment variable — resolve it by hand before migrating", p, imp)
			}
			pat := imp
			if !filepath.IsAbs(pat) {
				pat = filepath.Join(dir, pat)
			}
			matches, err := filepath.Glob(pat)
			if err != nil {
				return fmt.Errorf("%s: bad import glob %q: %w", p, imp, err)
			}
			sort.Strings(matches)
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
