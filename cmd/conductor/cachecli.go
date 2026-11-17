package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/NodeSpy/conductor/internal/repocache"
)

const cachesUsage = `usage: conductor sandbox caches [--config PATH]
       conductor sandbox caches clear [--force] [--config PATH] REPO [NAME [KEY]]

Shows the repository dependency caches conductor keeps for sandboxed agents
(sandbox.caches): each published cache's repository, name, key, size, when
a launch last restored it, and where its definition came from (config, the
repository's .conductor.yaml, or autodetect).

clear removes a repository's caches, one named cache, or one key. A cache a
running launch has an overlay on is kept unless --force (the launch
keeps the files it has open; a later lookup in it fails). It also forgets
that a cleared key's last warm was over max_size, so it is warmed again.
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
	force := false
	var rest []string
	for _, a := range args {
		if a == "--force" {
			force = true
			continue
		}
		rest = append(rest, a)
	}
	cfg, pos, err := loadConfig(rest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "conductor sandbox caches: %v\n", err)
		return 1
	}
	st := &repocache.Store{Dir: cfg.CachesDir()}
	if !clear {
		used := st.Used()
		if len(pos) > 0 {
			fmt.Fprintf(os.Stderr, "conductor sandbox caches: unexpected %q\n%s", pos[0], cachesUsage)
			return 2
		}
		if !cfg.CachesOn() {
			fmt.Println("repository caches are off (sandbox.caches.enabled is not true, or the agent sandbox is off)")
		}
		return printCaches(st, used)
	}
	if len(pos) < 1 || len(pos) > 3 {
		fmt.Fprint(os.Stderr, cachesUsage)
		return 2
	}
	repo := pos[0]
	if err := repocache.CheckRepo(repo); err != nil {
		fmt.Fprintf(os.Stderr, "conductor sandbox caches clear: %v\n", err)
		return 2
	}
	n, kept, failed := 0, 0, false
	// What is in use and its removal are one critical section with the
	// daemon's restores: a launch never gets a cache being removed.
	name, key := "", ""
	if len(pos) > 1 {
		name = pos[1]
	}
	if len(pos) > 2 {
		key = pos[2]
	}
	st.Locked(func() {
		// A cleared key that was over the cap is warmed again.
		st.ClearMarks(repo, name, key)
		used := st.Used()
		for _, e := range st.List() {
			if e.Repo != repo || (len(pos) > 1 && e.Name != pos[1]) || (len(pos) > 2 && e.Key != pos[2]) {
				continue
			}
			if used[e.Dir] && !force {
				fmt.Printf("kept %s %s %s: a running launch uses it (--force to remove anyway)\n", e.Repo, e.Name, e.Key)
				kept++
				continue
			}
			if err := st.Remove(e); err != nil {
				fmt.Fprintf(os.Stderr, "conductor sandbox caches clear: %s %s %s: %v\n", e.Repo, e.Name, e.Key, err)
				failed = true
				return
			}
			fmt.Printf("removed %s %s %s (%s)\n", e.Repo, e.Name, e.Key, repocache.HumanSize(e.Size))
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

func printCaches(st *repocache.Store, used map[string]bool) int {
	list := st.List()
	if len(list) == 0 {
		fmt.Printf("no caches in %s\n", st.Dir)
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REPO\tCACHE\tKEY\tSIZE\tLAST USED\tORIGIN\tIN USE")
	var total int64
	for _, e := range list {
		total += e.Size
		inUse := ""
		if used[e.Dir] {
			inUse = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Repo, e.Name, e.Key, repocache.HumanSize(e.Size), ago(e.LastUsed), e.Origin, inUse)
	}
	tw.Flush()
	fmt.Printf("%d cache(s), %s in %s\n", len(list), repocache.HumanSize(total), st.Dir)
	return 0
}
