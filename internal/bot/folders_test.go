package bot

import (
	"context"
	"reflect"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
)

// FOLDERS HAVE NO BEHAVIOR at runtime either: moving bots and teams between
// folders leaves every gate verdict, every peer link and every delegation
// decision exactly as it was.
func TestFoldersChangeNoRuntimeBehavior(t *testing.T) {
	ctx := context.Background()
	type outcome struct {
		Verdicts map[string]bool
		Linked   map[string]bool
		Delegate map[string]bool
		Class    string
	}
	run := func(edit func(*config.Config)) outcome {
		f := newTeamFixture(t, edit)
		o := outcome{Verdicts: map[string]bool{}, Linked: map[string]bool{}, Delegate: map[string]bool{}}
		names := f.cfg.BotNames()
		for _, n := range names {
			for _, verb := range []string{"pd.get_x", "pd.ack", "pd.get_secret", "gh.comment"} {
				v, err := f.svc.Gate.CheckVerb(ctx, flow.SkillIdentity{Bot: n, BotRole: config.BotRoleHome, Dispatch: f.homeOf(n).Dispatch}, verb, nil)
				o.Verdicts[n+" "+verb] = err == nil && v.Allow
			}
			for _, m := range names {
				o.Linked[n+">"+m] = f.cfg.BotLinked(n, m)
			}
		}
		for _, n := range names {
			for _, m := range names {
				_, err := f.svc.Delegate(ctx, f.homeOf(n), m, "task", "")
				o.Delegate[n+">"+m] = err == nil
			}
		}
		// A third party's request delegated keeps its class.
		g := newTeamFixture(t, edit)
		ac := g.deliver(t, Event{Bot: "a", Source: "chat.mention", Requester: "chat:U9", Title: "x"})
		bc, _ := g.delegate(t, "a", "b", ac, "x")
		o.Class = bc.Requester + "/" + bc.RequesterClass
		return o
	}
	base := run(nil)
	for name, edit := range map[string]func(*config.Config){
		"bots in folders": func(c *config.Config) {
			for n, f := range map[string]string{"a": "work/platform", "c": "personal/home", "solo": "work/platform", "lead": "x/y/z"} {
				m := c.Bots[n]
				m.Folder = f
				c.Bots[n] = m
			}
		},
		"teams in folders": func(c *config.Config) {
			for n, f := range map[string]string{"platform": "work/platform", "billing": "work/platform"} {
				tc := c.Teams[n]
				tc.Folder = f
				c.Teams[n] = tc
			}
		},
		"teams apart, solo with billing": func(c *config.Config) {
			tc := c.Teams["billing"]
			tc.Folder = "personal/home"
			c.Teams["billing"] = tc
			m := c.Bots["solo"]
			m.Folder = "personal/home"
			c.Bots["solo"] = m
		},
	} {
		if got := run(edit); !reflect.DeepEqual(got, base) {
			t.Errorf("%s changed behavior:\n got %+v\nwant %+v", name, got, base)
		}
	}
}
