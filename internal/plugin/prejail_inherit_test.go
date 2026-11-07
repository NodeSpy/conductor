package plugin

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// A fresh install a config migration names in Options.PreJail inherits the
// pre-jail mark: on a host that cannot jail plugins its install-time
// describe runs as before the jail instead of being refused, its record
// carries the mark, and so does its next build. MarkPreJail marks an
// installed one.
func TestInheritedPreJail(t *testing.T) {
	restore := SetHostJailForTests(HostJail{Reason: "conductor runs as root", Fix: "run it as another user"})
	defer restore()
	for _, inherit := range []bool{false, true} {
		st := stateAt(t)
		ref := refFor(t, config.UseKindConnector, "acme/conductor-plugins/jira@^1.0")
		var described Spec
		opts := Options{AllowUnlisted: true, Describe: func(_ context.Context, s Spec) (*Decl, error) {
			described = s
			return &Decl{}, nil
		}}
		if inherit {
			opts.PreJail = map[string]bool{"connectors/jira": true}
		}
		if _, err := Reconcile(map[string]config.PluginRef{ref.Key(): ref}, st, nil, stubFor("jira", "jira/v1.0.0"), opts); err != nil {
			t.Fatal(err)
		}
		rec, ok := st.Get("connectors/jira")
		if !ok {
			t.Fatal("not installed")
		}
		if _, before := runsAsBefore(described); before != inherit {
			t.Errorf("inherit=%v: its describe runs as before the jail: %v", inherit, before)
		}
		if rec.PreJail != inherit || RunsAsBefore(rec) != inherit {
			t.Errorf("inherit=%v: recorded pre_jail=%v", inherit, rec.PreJail)
		}
		// The next build carries the mark (Put).
		next := rec
		next.Resolved, next.Sha256, next.PreJail = "jira/v1.1.0", "ff", false
		st.Put(next)
		if got, _ := st.Get("connectors/jira"); got.PreJail != inherit {
			t.Errorf("inherit=%v: the next build's pre_jail=%v", inherit, got.PreJail)
		}
	}

	st := stateAt(t)
	st.Put(Installed{Key: "connectors/relay", Name: "relay", Resolved: "relay/v1.1.0"})
	st.Pending = append(st.Pending, Pending{Installed: Installed{Key: "connectors/relay", Name: "relay", Resolved: "relay/v1.2.0"}})
	if !st.MarkPreJail("connectors/relay") || st.MarkPreJail("connectors/relay") {
		t.Fatal("MarkPreJail does not report a change exactly once")
	}
	if rec, _ := st.Get("connectors/relay"); !rec.PreJail || !st.Pending[0].PreJail {
		t.Fatalf("not marked: %+v, %+v", rec, st.Pending[0])
	}
}
