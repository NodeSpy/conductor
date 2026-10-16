package config

import (
	"fmt"
	"strings"
)

// FOLDERS (docs/wiki/Bots.md#folders) organize bots and teams for display:
// `folder: work/platform` on a bot or a team files it there in
// `conductor bot ls`. That is ALL a folder does. It confers no permissions,
// defaults, isolation, peering or routing — teams own all behavior — so
// nothing outside display (BotFolder and the listings) may read Folder.

// Folder path bounds.
const (
	maxFolderDepth   = 8
	maxFolderSegment = 64
	maxFolderPath    = 256
)

// NormFolder validates a folder path and returns it normalized: segments
// trimmed and joined by "/". Empty is the root. A segment may not be empty,
// "." or "..", and holds only letters, digits, '.', '-' and '_'.
func NormFolder(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", nil
	}
	segs := strings.Split(p, "/")
	if len(segs) > maxFolderDepth {
		return "", fmt.Errorf("%q nests %d levels deep (max %d)", p, len(segs), maxFolderDepth)
	}
	for i, seg := range segs {
		seg = strings.TrimSpace(seg)
		switch {
		case seg == "":
			return "", fmt.Errorf("%q has an empty segment", p)
		case seg == "." || seg == "..":
			return "", fmt.Errorf("%q: %q is not a folder name", p, seg)
		case len(seg) > maxFolderSegment:
			return "", fmt.Errorf("%q: segment %q is longer than %d", p, seg, maxFolderSegment)
		}
		for _, r := range seg {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
				return "", fmt.Errorf("%q: segment %q may hold only letters, digits, '.', '-' and '_'", p, seg)
			}
		}
		segs[i] = seg
	}
	out := strings.Join(segs, "/")
	if len(out) > maxFolderPath {
		return "", fmt.Errorf("%q is longer than %d", p, maxFolderPath)
	}
	return out, nil
}

// BotFolder is the folder a bot is LISTED under: its team's, when it is
// on a team (a bot's own folder: is then ignored, with a validate
// warning), else its own. Display only.
func (c *Config) BotFolder(name string) string {
	m := c.Bots[name]
	p := m.Folder
	if t, ok := c.Teams[string(m.Team)]; ok {
		p = t.Folder
	}
	f, _ := NormFolder(p)
	return f
}

// TeamFolder is the folder a team is listed under. Display only.
func (c *Config) TeamFolder(name string) string {
	f, _ := NormFolder(c.Teams[name].Folder)
	return f
}

// FolderWarnings flags a bot on a team that sets its own folder: the
// team's wins for display.
func (c *Config) FolderWarnings() []string {
	var out []string
	for _, name := range c.BotNames() {
		m := c.Bots[name]
		if strings.TrimSpace(m.Folder) == "" || m.Team == "" {
			continue
		}
		if _, ok := c.Teams[string(m.Team)]; !ok {
			continue
		}
		tf := c.TeamFolder(string(m.Team))
		if tf == "" {
			tf = "the root"
		}
		out = append(out, fmt.Sprintf("bots.%s: folder %q is ignored — %s is on team %s, and a bot on a team is listed under its team's folder (%s)", name, m.Folder, name, m.Team, tf))
	}
	return out
}
