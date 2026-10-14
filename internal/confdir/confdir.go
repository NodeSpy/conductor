// Package confdir resolves where conductor's configuration lives when no
// --config flag names it. It is a leaf package so the CLI, the service-unit
// renderer and the secrets vault all resolve the same directory.
//
// Order:
//
//  1. $CONDUCTOR_CONFIG — the config file, or an existing directory holding
//     config.yaml. A leading "~/" expands to the home directory; a relative
//     path resolves against the working directory.
//  2. $XDG_CONFIG_HOME/conductor/config.yaml, when XDG_CONFIG_HOME is set to an
//     absolute path (the XDG spec ignores a relative one).
//  3. ~/.config/conductor/config.yaml — the XDG default, spelled out so a box
//     without the variable behaves exactly as before.
package confdir

import (
	"os"
	"path/filepath"
	"strings"
)

// Env names the override variable.
const Env = "CONDUCTOR_CONFIG"

// FileName is the config file inside a config directory.
const FileName = "config.yaml"

// File returns the config file conductor reads by default.
func File() string {
	if v := strings.TrimSpace(os.Getenv(Env)); v != "" {
		p := expand(v)
		if strings.HasSuffix(v, "/") || isDir(p) {
			return filepath.Join(p, FileName)
		}
		return p
	}
	return filepath.Join(xdgDir(), FileName)
}

// Dir returns the directory holding File(): where conductor.env, the vault,
// packs and the lockfile live alongside the config.
func Dir() string { return filepath.Dir(File()) }

// Default reports the config file conductor reads when neither the override
// nor XDG_CONFIG_HOME is set: ~/.config/conductor/config.yaml.
func Default() string { return filepath.Join(home(), ".config", "conductor", FileName) }

// IsDefault reports whether File() is the plain default, i.e. nothing in the
// environment moved it. A service unit carries the resolved location only when
// it is not, so an existing unit keeps its exact content.
func IsDefault() bool { return File() == Default() }

func xdgDir() string {
	if x := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "conductor")
	}
	return filepath.Join(home(), ".config", "conductor")
}

func expand(p string) string {
	if p == "~" {
		return home()
	}
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(home(), p[2:])
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
