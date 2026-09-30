package hostcmd

import "strings"

// flagSpec describes one tool's flags well enough to separate flag values
// from positional arguments — the part that makes rule matching independent
// of flag order and spelling.
type flagSpec struct {
	// value names the long flags that take a value ("--repo").
	value map[string]bool
	// alias maps short flags to their long name ("-R" → "--repo").
	alias map[string]string
	// unknownTakesValue: an unknown `--flag` followed by a non-flag token
	// takes it as its value (aws operation parameters are all `--name value`).
	unknownTakesValue bool
	// boolean overrides unknownTakesValue for known boolean long flags.
	boolean map[string]bool
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// parseFlags splits args into positionals and a normalized flag map.
// `--x=y`, `--x y` (x a value flag), `-X y`, `-Xy` (X a short value flag) and
// `-X=y` all record flags["--x"] = [y]. `--` ends flag parsing.
func parseFlags(args []string, spec flagSpec) (pos []string, flags map[string][]string) {
	flags = map[string][]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		if !strings.HasPrefix(a, "--") && len(a) > 2 && a[2] != '=' {
			// A short-flag cluster: -it, -At, -Rowner/repo, -oProxyCommand=x.
			// Each letter is a flag until one takes a value, which is then the
			// rest of the token (or the next argument).
			rest := a[1:]
			for len(rest) > 0 {
				key := spec.shortKey("-" + rest[:1])
				rest = rest[1:]
				if spec.value[key] {
					if rest != "" {
						flags[key] = append(flags[key], rest)
					} else if i+1 < len(args) {
						flags[key] = append(flags[key], args[i+1])
						i++
					} else {
						flags[key] = append(flags[key], "")
					}
					rest = ""
					break
				}
				flags[key] = append(flags[key], "")
			}
			continue
		}
		name, val, hasVal := strings.Cut(a, "=")
		long := name
		if !strings.HasPrefix(name, "--") {
			long = spec.shortKey(name) // -R, -R=value
		}
		if hasVal {
			flags[long] = append(flags[long], val)
			continue
		}
		takes := spec.value[long]
		if !takes && spec.unknownTakesValue && strings.HasPrefix(long, "--") && !spec.boolean[long] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			takes = true
		}
		if takes && i+1 < len(args) {
			flags[long] = append(flags[long], args[i+1])
			i++
			continue
		}
		flags[long] = append(flags[long], "")
	}
	return pos, flags
}

// shortKey is the name a short flag is recorded under: its long alias when
// known, else itself.
func (s flagSpec) shortKey(short string) string {
	if l, ok := s.alias[short]; ok {
		return l
	}
	return short
}

// normFlag is the flag name a rule pattern is compared under (rules name
// flags by their long form).
func normFlag(s string) string { return s }

// argvWords is the matching view of a binary without a profile: the raw
// argv as words, plus whatever `--flag[=value]` tokens it carries.
func argvWords(args []string) ([]string, map[string][]string) {
	flags := map[string][]string{}
	for _, a := range args {
		if strings.HasPrefix(a, "-") && a != "-" {
			n, v, _ := strings.Cut(a, "=")
			flags[n] = append(flags[n], v)
		}
	}
	return append([]string(nil), args...), flags
}

// parseGeneric is the Parsed of a binary without a profile.
func parseGeneric(args []string) Parsed {
	w, f := argvWords(args)
	return Parsed{Words: w, Flags: f}
}

func (p Parsed) has(flag string) bool {
	_, ok := p.Flags[flag]
	return ok
}

func (p Parsed) first(flag string) string {
	if v := p.Flags[flag]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func (p Parsed) word(i int) string {
	if i < len(p.Words) {
		return p.Words[i]
	}
	return ""
}

// profile is a built-in tool profile.
type profile interface {
	parse(args []string, ctx Context) Parsed
}

// Profile metadata the runner needs: which home paths the tool's host-side
// process may see (copy-on-write), which of those write through.
type homeView struct {
	// Paths are home-relative files/dirs the tool reads its config and
	// credentials from; only these are visible (copy-on-write) to its
	// host-side process. nil = the whole home (a binary with no profile).
	Paths []string
	// Persist are home-relative paths whose writes survive (rotating
	// refresh tokens, e.g. .aws/sso/cache).
	Persist []string
	// Env are fixed environment settings for the host-side process.
	Env map[string]string
}

var profiles = map[string]profile{}
var views = map[string]homeView{}

func register(name string, p profile, v homeView) {
	profiles[name] = p
	views[name] = v
}

func profileFor(tool string) profile { return profiles[tool] }

// commonHome is what every profiled tool may see besides its own paths: git
// identity (gh reads the remote from the workspace's git config) and the
// XDG config for git.
var commonHome = []string{".gitconfig", ".config/git"}

// HomeView returns the home paths tool's host-side process may see (nil =
// all of home, for a binary without a profile), its persist paths, and its
// fixed env. extraPersist are the operator's configured persist paths
// (`~/…`).
func HomeView(tool string, extraPersist []string) (paths, persist []string, env map[string]string, full bool) {
	v, ok := views[tool]
	for _, p := range extraPersist {
		persist = append(persist, strings.TrimPrefix(p, "~/"))
	}
	if !ok {
		return nil, persist, nil, true
	}
	paths = append(append([]string(nil), commonHome...), v.Paths...)
	persist = append(append([]string(nil), v.Persist...), persist...)
	return paths, persist, v.Env, false
}
