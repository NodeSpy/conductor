package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// META-TEST: the legacy config schema (docs/design/plugin-contract.md
// decision Q4, §3 rows V3/G17/G18) was removed in this release —
// `integrations:`, `notify:`, `handoff:`/`handoffs:`, `controllers:`,
// `control:`, and `paseo_bin` are gone, and Validate names any of them still
// present in a config (see checkLegacyBlocks). This test guards against the
// schema creeping back: no non-test file under internal/ or cmd/ may
// reference the removed types, or read the removed fields off a live
// *Config, outside this package's own legacyBlock placeholders.
//
// It is deliberately a text scan, not just reliance on the compiler: the
// point is a readable, explicit tripwire that names the exact pattern that
// must not come back, not merely "it happens not to compile today".
func TestNoLegacySchemaReferencesOutsideConfigPackage(t *testing.T) {
	root := repoRootForLegacyMeta(t)

	// Removed exported types. Each is a unique identifier that cannot
	// legitimately appear anywhere outside internal/config (where they no
	// longer exist either, except in doc comments).
	removedTypes := []string{
		"config.IntegrationRef",
		"config.HandoffConfig",
		"config.HandoffWeb",
		"config.HandoffChat",
		"config.NotifyRoute",
		"config.NotifyNtfy",
		"config.NotifyPushover",
		"config.NotifyNotifiarr",
	}

	// Removed field accesses off a *Config/Config value. Scoped to the
	// receiver names this codebase actually uses for one in hand (cfg/c) —
	// "PaseoBin" and "Integrations" are deliberately NOT included here: they
	// collide with live, unrelated fields of the same name elsewhere
	// (dispatch.Dispatcher.PaseoBin; flowStack.Integrations, the
	// connectors-model lowered integration list) that a bare `.Integrations`/
	// `.PaseoBin` word-boundary match can't tell apart from a resurrected
	// config.Config field — the compiler already guards those two.
	removedFieldPatterns := []*regexp.Regexp{
		regexp.MustCompile(`cfg\.Integrations\b`),
		regexp.MustCompile(`c\.Integrations\b`),
		regexp.MustCompile(`cfg\.PaseoBin\b`),
		regexp.MustCompile(`c\.PaseoBin\b`),
		regexp.MustCompile(`cfg\.Control\.`), // field access, not net.Conn's .Control(fn)
		regexp.MustCompile(`c\.Control\.`),
		regexp.MustCompile(`cfg\.Controllers\b`), // the removed top-level block (ControllerConfig the TYPE stays)
		regexp.MustCompile(`c\.Controllers\b`),
		regexp.MustCompile(`cfg\.Handoffs\b`),
		regexp.MustCompile(`c\.Handoffs\b`),
		regexp.MustCompile(`cfg\.Handoff\.Web\b`),
		regexp.MustCompile(`c\.Handoff\.Web\b`),
	}

	err := filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, "internal/") && !strings.HasPrefix(rel, "cmd/") {
			return nil
		}
		// internal/config/connectors.go legitimately decodes a RuntimeConfig's
		// pre-use: `type:` into a field called `legacy`/`legacyType`, and this
		// package's own config.go defines the legacyBlock placeholders
		// checkLegacyBlocks reads — both are the removal's own machinery, not
		// a resurrection of it.
		if strings.HasPrefix(rel, "internal/config/") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		src := string(b)
		for _, bad := range removedTypes {
			if strings.Contains(src, bad) {
				t.Errorf("%s: references removed type %q (legacy config schema) — plugin-contract.md decision Q4", rel, bad)
			}
		}
		for _, re := range removedFieldPatterns {
			if re.MatchString(src) {
				t.Errorf("%s: matches removed legacy config field pattern %q", rel, re.String())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// repoRootForLegacyMeta walks up to the module root.
func repoRootForLegacyMeta(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("module root not found")
	return ""
}
