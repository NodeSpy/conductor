package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/NodeSpy/conductor/internal/keycache"
)

const cachesUsage = `usage: conductor sandbox caches [--config PATH]
       conductor sandbox caches clear [--force] [--config PATH] KEY...
       conductor sandbox caches clear --all [--force] [--config PATH]

Shows the step caches conductor keeps for sandboxed agents (sandbox.caches):
each key (as its step's cache: template rendered, with the pack instance
that declared it), the template, the paths it learned, the size of its
newest published copy, when a launch last restored it, and whether a running
launch uses it.

clear removes a key whole: its published copies and the paths it learned
(KEY is the key as shown, its rendered value, or its id). A copy a running
launch has an overlay on is kept unless --force (the launch keeps the files
it has open; a later lookup in it fails).
`

// cmdSandboxCaches is `conductor sandbox caches`.
func cmdSandboxCaches(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Fprint(os.Stdout, cachesUsage)
			return 0
		}
	}
	clear := len(args) > 0 && args[0] == "clear"
	if clear {
		args = args[1:]
	}
	force, all := false, false
	var rest []string
	for _, a := range args {
		switch a {
		case "--force":
			force = true
		case "--all":
			all = true
		default:
			rest = append(rest, a)
		}
	}
	cfg, pos, err := loadConfig(rest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "conductor sandbox caches: %v\n", err)
		return 1
	}
	st := &keycache.Store{Dir: cfg.CachesDir()}
	if !clear {
		if len(pos) > 0 || all {
			extra := "--all"
			if len(pos) > 0 {
				extra = pos[0]
			}
			fmt.Fprintf(os.Stderr, "conductor sandbox caches: unexpected %q\n%s", extra, cachesUsage)
			return 2
		}
		if !cfg.CachesOn() {
			fmt.Println("step caches are off (sandbox.caches.enabled is not true, or the agent sandbox is off)")
		}
		return printCaches(st, st.Used())
	}
	if all == (len(pos) > 0) {
		fmt.Fprint(os.Stderr, cachesUsage)
		return 2
	}
	n, kept, failed := 0, 0, false
	// What is in use and its removal are one critical section with the
	// daemon's restores: a launch never gets a copy being removed.
	st.Locked(func() {
		used := st.Used()
		for _, k := range st.Keys() {
			match := all
			for _, sel := range pos {
				match = match || k.Matches(sel)
			}
			if !match {
				continue
			}
			removed, keptOne, err := st.ClearKey(k.ID, func(d string) bool { return used[d] }, force)
			for _, e := range removed {
				fmt.Printf("removed %s %s (%s)\n", k.Key, e.Version, keycache.HumanSize(e.Size))
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "conductor sandbox caches clear: %s: %v\n", k.Key, err)
				failed = true
				return
			}
			if keptOne {
				fmt.Printf("kept %s: a running launch uses it (--force to remove anyway)\n", k.Key)
				kept++
				continue
			}
			fmt.Printf("cleared %s\n", k.Key)
			n++
		}
	})
	if failed {
		return 1
	}
	if n == 0 && kept == 0 {
		fmt.Println("no matching cache")
	}
	if kept > 0 {
		return 1
	}
	return 0
}

func printCaches(st *keycache.Store, used map[string]bool) int {
	keys := st.Keys()
	if len(keys) == 0 {
		fmt.Printf("no caches in %s\n", st.Dir)
		return 0
	}
	newest := map[string]keycache.Entry{}
	var total int64
	versions := 0
	for _, e := range st.List() {
		total += e.Size
		versions++
		if cur, ok := newest[e.ID]; !ok || e.Version > cur.Version {
			newest[e.ID] = e
		}
	}
	inUse := map[string]bool{}
	for _, e := range st.List() {
		if used[e.Dir] {
			inUse[e.ID] = true
		}
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tTEMPLATE\tPATHS\tSIZE\tLAST USED\tIN USE")
	for _, k := range keys {
		size, last := "-", "-"
		if e, ok := newest[k.ID]; ok {
			size, last = keycache.HumanSize(e.Size), ago(e.LastUsed)
		}
		paths := "(learning)"
		if len(k.Learned) > 0 {
			paths = "~/" + strings.Join(k.Learned, " ~/")
		}
		yes := ""
		if inUse[k.ID] {
			yes = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", k.Key, k.Key.Template, paths, size, last, yes)
	}
	tw.Flush()
	fmt.Printf("%d key(s), %d published cop(ies), %s in %s\n", len(keys), versions, keycache.HumanSize(total), st.Dir)
	return 0
}
