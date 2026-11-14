package config

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// validateCommands checks the top-level `commands:` block: each key is a
// command name as a plugin declares it (no path), each value an absolute
// path (after ~ expansion) to the program file. Whether the file is there,
// and may run jailed, is checked where it runs: at the plugin's launch.
func (c *Config) validateCommands() error {
	names := make([]string, 0, len(c.Commands))
	for n := range c.Commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := c.Commands[n]
		if n == "" || strings.ContainsAny(n, "/\\ \t") || n == "." || n == ".." {
			return fmt.Errorf("config: commands: %q is not a command name — a key is the name a plugin declares (gh, git), and its value the program file", n)
		}
		if !filepath.IsAbs(p) {
			return fmt.Errorf("config: commands.%s: %q is not an absolute path — name the program file, e.g. /opt/homebrew/Cellar/%s/<version>/bin/%s", n, p, n, n)
		}
		if filepath.Base(filepath.Clean(p)) != n {
			return fmt.Errorf("config: commands.%s: %s is not named %s — a plugin runs the command by its name, so name a file called %s", n, p, n, n)
		}
	}
	return nil
}

// CommandPaths is the `commands:` block: the program file the config names
// for a declared command, by command name (nil when there is none).
func (c *Config) CommandPaths() map[string]string {
	if len(c.Commands) == 0 {
		return nil
	}
	out := make(map[string]string, len(c.Commands))
	for n, p := range c.Commands {
		out[n] = filepath.Clean(p)
	}
	return out
}
