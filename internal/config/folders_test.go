package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormFolder(t *testing.T) {
	for in, want := range map[string]string{
		"":                      "",
		"   ":                   "",
		"work":                  "work",
		"work/platform":         "work/platform",
		" work / platform ":     "work/platform",
		"personal/home":         "personal/home",
		"a.b/c-d/e_f/G9":        "a.b/c-d/e_f/G9",
		"a/b/c/d/e/f/g/h":       "a/b/c/d/e/f/g/h",
		strings.Repeat("x", 64): strings.Repeat("x", 64),
		strings.Repeat(strings.Repeat("y", 60)+"/", 4) + "zzzzzzzzzzzz": strings.Repeat(strings.Repeat("y", 60)+"/", 4) + "zzzzzzzzzzzz", // 256
	} {
		got, err := NormFolder(in)
		if err != nil || got != want {
			t.Errorf("NormFolder(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"/work", "work/", "work//platform", "work/ /platform", // empty segments
		"..", "work/..", "../etc", ".", "work/./x", // dot segments
		"work platform", "work/plat form", "wörk", "work\\platform", "work:x", "work/*", "~/x", // charset
		strings.Repeat("x", 65), // segment length
		"a/b/c/d/e/f/g/h/i",     // depth
		strings.Repeat(strings.Repeat("y", 60)+"/", 4) + "zzzzzzzzzzzzz", // total length: 257
	} {
		if got, err := NormFolder(in); err == nil {
			t.Errorf("NormFolder(%q) = %q, want an error", in, got)
		}
	}
}

func TestFoldersAreValidatedAtLoad(t *testing.T) {
	if _, err := loadTeams(t, "bots:\n  a: { folder: \"work/../x\" }\n"); err == nil || !strings.Contains(err.Error(), "bots.a: folder") {
		t.Errorf("a bot's bad folder: %v", err)
	}
	if _, err := loadTeams(t, "teams:\n  x: { folder: \"work//x\" }\nbots:\n  a: { team: x }\n"); err == nil || !strings.Contains(err.Error(), "folder") {
		t.Errorf("a team's bad folder: %v", err)
	}
}

func TestBotFolderTeamWinsWithAWarning(t *testing.T) {
	c, err := loadTeams(t, "teams:\n  x: { folder: work/platform }\n  y: {}\nbots:\n  a: { team: x, folder: personal/home }\n  b: { team: x }\n  s: { folder: \" personal / home \" }\n  r: {}\n  n: { team: y, folder: misc }\n")
	if err != nil {
		t.Fatal(err)
	}
	for bot, want := range map[string]string{"a": "work/platform", "b": "work/platform", "s": "personal/home", "r": "", "n": ""} {
		if got := c.BotFolder(bot); got != want {
			t.Errorf("BotFolder(%s) = %q, want %q", bot, got, want)
		}
	}
	if c.TeamFolder("x") != "work/platform" || c.TeamFolder("y") != "" {
		t.Errorf("team folders: %q %q", c.TeamFolder("x"), c.TeamFolder("y"))
	}
	w := strings.Join(c.BotWarnings(), "\n")
	if !strings.Contains(w, `bots.a: folder "personal/home" is ignored`) || !strings.Contains(w, "(work/platform)") {
		t.Errorf("no warning for a's own folder: %s", w)
	}
	if !strings.Contains(w, `bots.n: folder "misc" is ignored`) || !strings.Contains(w, "(the root)") {
		t.Errorf("no warning for n's own folder: %s", w)
	}
	if strings.Contains(w, "bots.b: folder") || strings.Contains(w, "bots.s: folder") || strings.Contains(w, "bots.r: folder") {
		t.Errorf("a folder warning where none is due: %s", w)
	}
}

// FOLDERS HAVE NO BEHAVIOR: the same config with the bots and teams moved
// between folders resolves to the same effective bots and teams, the same
// peering and operators.
func TestFoldersChangeNothing(t *testing.T) {
	const body = "teams:\n  x: { %TX% operators: [ops], peers: team, autonomy: { ceiling: reversible }, isolation: { handoff_workers: false } }\n" +
		"bots:\n  a: { %A% team: x }\n  b: { %B% team: x, autonomy: { promote: [kv.set] } }\n  s: { %S% peers: [t] }\n  t: { %T% peers: [s] }\n  u: { %U% }\n"
	render := func(f map[string]string) string {
		s := body
		for _, k := range []string{"TX", "A", "B", "S", "T", "U"} {
			v := ""
			if f[k] != "" {
				v = "folder: " + f[k] + ","
			}
			s = strings.ReplaceAll(s, "%"+k+"%", v)
		}
		return s
	}
	base, err := loadTeams(t, render(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []map[string]string{
		{"A": "work/platform", "S": "work/platform", "U": "personal/home"},
		{"TX": "work/platform", "S": "personal/home", "T": "work", "U": "work/platform"},
		{"TX": "a/b/c", "A": "z", "B": "y", "S": "s", "T": "t", "U": "u"},
	} {
		c, err := loadTeams(t, render(f))
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range base.BotNames() {
			got, want := c.Bots[n], base.Bots[n]
			got.Folder, want.Folder = "", ""
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%v: bot %s's effective config changed:\n got %+v\nwant %+v", f, n, got, want)
			}
			if !reflect.DeepEqual(c.BotOperators(n), base.BotOperators(n)) || c.MaxDelegationDepth(n) != base.MaxDelegationDepth(n) {
				t.Errorf("%v: bot %s's operators or delegation depth changed", f, n)
			}
			for _, m := range base.BotNames() {
				if c.BotLinked(n, m) != base.BotLinked(n, m) {
					t.Errorf("%v: BotLinked(%s, %s) changed", f, n, m)
				}
			}
		}
		for n := range base.Teams {
			got, want := c.Teams[n], base.Teams[n]
			got.Folder, want.Folder = "", ""
			if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(c.TeamBots(n), base.TeamBots(n)) {
				t.Errorf("%v: team %s changed", f, n)
			}
		}
	}
}
