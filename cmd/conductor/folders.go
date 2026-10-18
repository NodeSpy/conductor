package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
)

// folderEntry is one thing filed in a folder: a bot, a team, a workflow or
// a trigger. Folders are display only — this is all they are for.
type folderEntry struct {
	folder, kind, name, detail string
}

// takeFolderFlags strips --folder <path> and --flat from args.
func takeFolderFlags(args []string) (rest []string, folder string, flat bool, err error) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--flat":
			flat = true
		case a == "--folder" && i+1 < len(args):
			folder = args[i+1]
			i++
		case strings.HasPrefix(a, "--folder="):
			folder = strings.TrimPrefix(a, "--folder=")
		default:
			rest = append(rest, a)
		}
	}
	folder, err = config.NormFolder(folder)
	if err != nil {
		err = fmt.Errorf("--folder: %w", err)
	}
	return rest, folder, flat, err
}

// workflowEntries are the config's workflows and triggers, where they are
// listed (a pack's under packs/<instance> unless the operator filed them, a
// bot's workers under the bot's folder).
func workflowEntries(cfg *config.Config) []folderEntry {
	var out []folderEntry
	for name, w := range cfg.Workflows {
		out = append(out, folderEntry{cfg.WorkflowFolder(name), "workflow", name, w.Description})
	}
	for i, t := range cfg.Triggers {
		if t.Abstract {
			continue
		}
		name := t.Name
		if name == "" {
			name = fmt.Sprintf("#%d", i+1)
		}
		detail := "on: " + t.On
		if t.Enabled != nil && !*t.Enabled {
			detail += " (disabled)"
		}
		if t.Bot != "" {
			detail += " — bot " + t.Bot
		} else if t.PackInstance != "" {
			detail += " — pack " + t.PackInstance
		}
		out = append(out, folderEntry{cfg.TriggerFolder(t), "trigger", name, detail})
	}
	return out
}

// botTeamEntries are the config's bots and teams, where they are listed.
func botTeamEntries(cfg *config.Config) []folderEntry {
	var out []folderEntry
	for _, n := range cfg.TeamNames() {
		out = append(out, folderEntry{cfg.TeamFolder(n), "team", n, cfg.Teams[n].Description})
	}
	for _, n := range cfg.BotNames() {
		d := cfg.Bots[n].Description
		if t := cfg.Bots[n].Team; t != "" {
			d = strings.TrimSpace("team " + string(t) + " — " + d)
		}
		out = append(out, folderEntry{cfg.BotFolder(n), "bot", n, d})
	}
	return out
}

// inFolder reports whether f is folder or under it ("" is everything).
func inFolder(f, folder string) bool {
	return folder == "" || f == folder || strings.HasPrefix(f, folder+"/")
}

var kindOrder = map[string]int{"team": 0, "bot": 1, "workflow": 2, "trigger": 3}

// folderTreeText renders entries as a tree of folders (or, flat, one line
// each), keeping those in folder and below it.
func folderTreeText(entries []folderEntry, folder string, flat bool) string {
	var es []folderEntry
	for _, e := range entries {
		if inFolder(e.folder, folder) {
			es = append(es, e)
		}
	}
	if len(es) == 0 {
		if folder != "" {
			return fmt.Sprintf("nothing in folder %s\n", folder)
		}
		return "nothing to list\n"
	}
	sort.Slice(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.folder != b.folder {
			return a.folder < b.folder
		}
		if kindOrder[a.kind] != kindOrder[b.kind] {
			return kindOrder[a.kind] < kindOrder[b.kind]
		}
		return a.name < b.name
	})
	var sb strings.Builder
	if flat {
		for _, e := range es {
			fmt.Fprintf(&sb, "%-8s %-28s %-24s %s\n", e.kind, e.name, orRoot(e.folder), e.detail)
		}
		return sb.String()
	}
	var printed []string
	last := "\x00"
	for _, e := range es {
		var segs []string
		if e.folder != "" {
			segs = strings.Split(e.folder, "/")
		}
		if e.folder != last {
			common := 0
			for common < len(printed) && common < len(segs) && printed[common] == segs[common] {
				common++
			}
			for d := common; d < len(segs); d++ {
				fmt.Fprintf(&sb, "%s%s/\n", strings.Repeat("  ", d), segs[d])
			}
			printed, last = segs, e.folder
		}
		line := fmt.Sprintf("%s%-8s %s", strings.Repeat("  ", len(segs)), e.kind, e.name)
		if e.detail != "" {
			line += "  — " + e.detail
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

// cmdFolders is `conductor folders ls [path] [--flat]`: everything filed in
// a folder — teams, bots, workflows, triggers — read from config.
func cmdFolders(args []string) error {
	args, folder, flat, err := takeFolderFlags(args)
	if err != nil {
		return err
	}
	cfg, _, err := loadConfig(args)
	if err != nil {
		return err
	}
	rest := positional(args)
	if len(rest) > 0 && rest[0] == "ls" {
		rest = rest[1:]
	}
	if len(rest) > 1 {
		return fmt.Errorf("usage: conductor folders ls [path] [--flat]")
	}
	if len(rest) == 1 {
		if folder, err = config.NormFolder(rest[0]); err != nil {
			return err
		}
	}
	fmt.Print(folderTreeText(append(botTeamEntries(cfg), workflowEntries(cfg)...), folder, flat))
	return nil
}
