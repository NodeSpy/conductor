package configedit

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// dashboard comes last, so a test can append keys to it.
const guardedSample = `operators:
  ops:
    ssh_key: ssh-ed25519 AAAA victim
pricing:
  default: {input: 1, output: 2}
usage:
  mode: off
store:
  state_file: /var/lib/conductor/state.json
bots:
  b1:
    description: one
dashboard:
  enabled: true
  listen: [localhost]
`

// Whether an edit needs a step-up is decided on the whole candidate, not on the
// path that was staged: a loosening inside a parent map or list is caught, and
// anything outside the inert paths (pricing, usage.mode) is guarded: who can
// sign in and how, the store, and everything that runs on the box.
func TestClassifyWholeCandidate(t *testing.T) {
	for _, c := range []struct {
		name    string
		after   string
		widens  string // a widening must name this path ("" = none)
		guarded bool
	}{
		{"swap an ssh key", strings.Replace(guardedSample, "AAAA victim", "AAAA attacker", 1), "", true},
		{"add an operator", strings.Replace(guardedSample, "operators:\n", "operators:\n  mallory: {ssh_key: ssh-ed25519 AAAA m}\n", 1), "", true},
		{"remove the operators", strings.Replace(guardedSample, "operators:\n  ops:\n    ssh_key: ssh-ed25519 AAAA victim\n", "", 1), "", true},
		{"auth as a flow map", guardedSample + "  auth: {password_requires_totp: false, stepup: [passkey, ssh, totp, password]}\n", "dashboard.auth.password_requires_totp", true},
		{"bind as a list", guardedSample + "  bind: [{address: 0.0.0.0:7791, hostnames: [box.lan], allow_plain_http: true}]\n", "dashboard.bind.0.allow_plain_http", true},
		{"tailscale tls off", guardedSample + "  tailscale: {tls: off}\n", "dashboard.tailscale.tls", true},
		{"listen widened", strings.Replace(guardedSample, "[localhost]", "[localhost, tailscale]", 1), "", true},
		{"session", guardedSample + "  session: {idle: 10m}\n", "", true},
		{"add an import", "imports: [more.yaml]\n" + guardedSample, "", true},
		{"unparsable", guardedSample + "  : [\n", "", true},
		{"sandbox off via its parent", "sandbox: {agents: off}\n" + guardedSample, "sandbox.agents", true},
		{"a bot's description", strings.Replace(guardedSample, "description: one", "description: two", 1), "", true},
		{"store.state_file", strings.Replace(guardedSample, "/var/lib/conductor/state.json", "/tmp/x/state.json", 1), "", true},
		{"dashboard.enabled", strings.Replace(guardedSample, "enabled: true", "enabled: false", 1), "", true},
		{"dashboard.port", guardedSample + "  port: 7000\n", "", true},
		{"config_stepup guarded", guardedSample + "  auth: {config_stepup: guarded}\n", "dashboard.auth.config_stepup", true},
		{"a cron connector and a cli trigger", reviewerPayload, "", true},
		{"a key conductor doesn't know yet", "brand_new: {x: 1}\n" + guardedSample, "", true},
		{"the whole config a list", "- a\n- b\n", "", true},
		{"usage sources", strings.Replace(guardedSample, "mode: off", "mode: off\n  sources: [{name: s, type: http, url: http://x}]", 1), "", true},
		{"pricing", strings.Replace(guardedSample, "input: 1", "input: 3", 1), "", false},
		{"usage.mode", strings.Replace(guardedSample, "mode: off", "mode: api", 1), "", false},
		{"no change", guardedSample, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			widening, guarded := classify([]byte(guardedSample), []byte(c.after))
			if c.widens == "" && len(widening) > 0 {
				t.Fatalf("unexpected widening: %+v", widening)
			}
			if c.widens != "" {
				found := false
				for _, w := range widening {
					found = found || w.Path == c.widens
				}
				if !found {
					t.Fatalf("no widening at %s: %+v", c.widens, widening)
				}
			}
			if got := len(guarded) > 0; got != c.guarded {
				t.Fatalf("guarded = %v, want %v: %+v", got, c.guarded, guarded)
			}
		})
	}
}

// reviewerPayload is a cron connector firing a trigger whose step runs a shell
// command: no widening leaf, nothing under operators or the dashboard, and
// still a shell on the box.
var reviewerPayload = guardedSample + `connectors:
  tick:
    use: cron
    schedules: {x: {every: 15s, run_on_start: true}}
triggers:
  - on: tick.x
    steps: [{id: pwn, use: cli, command: [sh, -c, id > /tmp/pwned]}]
`

// The guarded effects name what runs, most specific first.
func TestClassifyNamesWhatRuns(t *testing.T) {
	_, guarded := classify([]byte(guardedSample), []byte(reviewerPayload))
	var paths []string
	for _, g := range guarded {
		paths = append(paths, g.Path)
	}
	if strings.Join(paths, ",") != "triggers,connectors" {
		t.Fatalf("guarded = %v, want triggers then connectors", paths)
	}
	_, guarded = classify([]byte("{}"), []byte("a: 1\n"))
	if len(guarded) != 1 || guarded[0].Path != "a" {
		t.Fatalf("from an empty map: %+v", guarded)
	}
	_, guarded = classify([]byte("a: 1\n"), []byte("{}"))
	if len(guarded) != 1 || guarded[0].Path != "a" {
		t.Fatalf("to an empty map: %+v", guarded)
	}
}

// Through the pipeline: the review says so, and Apply asks its gate with the
// same answer, on the candidate it writes.
func TestStepupThroughPipeline(t *testing.T) {
	sample := guardedSample + "  session:\n    idle: 30m\n"
	for _, c := range []struct {
		name   string
		change Change
		stepup bool
	}{
		{"swap an ssh key", Change{Path: []string{"operators", "ops", "ssh_key"}, Value: "ssh-ed25519 AAAA attacker"}, true},
		{"remove an operator", Change{Path: []string{"operators", "ops"}, Delete: true}, true},
		{"session idle", Change{Path: []string{"dashboard", "session", "idle"}, Value: "10m"}, true},
		{"a bot's description", Change{Path: []string{"bots", "b1", "description"}, Value: "two"}, true},
		{"pricing", Change{Path: []string{"pricing", "default", "input"}, Value: 3}, false},
		{"usage.mode", Change{Path: []string{"usage", "mode"}, Value: "api"}, false},
		{"a cron connector", Change{Path: []string{"connectors", "tick"}, Value: map[string]any{"use": "cron", "schedules": map[string]any{"x": map[string]any{"every": "15s", "run_on_start": true}}}}, true},
		{"a cli trigger", Change{Path: []string{"triggers"}, Value: []any{map[string]any{"on": "tick.x", "steps": []any{map[string]any{"id": "pwn", "use": "cli", "command": []any{"sh", "-c", "id"}}}}}}, true},
		// New sections: these used to fail with "re-parse after edit" instead of
		// reaching review; they must now review cleanly and still need a step-up.
		{"add an operator", Change{Path: []string{"operators", "mallory"}, Value: map[string]any{"ssh_key": "ssh-ed25519 AAAA m"}}, true},
		{"replace all operators", Change{Path: []string{"operators"}, Value: map[string]any{"ops": map[string]any{"ssh_key": "ssh-ed25519 AAAA x"}}}, true},
		{"add dashboard.auth", Change{Path: []string{"dashboard", "auth"}, Value: map[string]any{"password_requires_totp": false}}, true},
		{"a leaf under a new section", Change{Path: []string{"dashboard", "auth", "methods"}, Value: []any{"ssh"}}, true},
		{"a new bot section", Change{Path: []string{"bots", "b2"}, Value: map[string]any{"description": "new"}}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _, _ := newPipe(t, sample, nil)
			if _, err := p.Stage(c.change); err != nil {
				t.Fatal(err)
			}
			rev, err := p.Review()
			if err != nil {
				t.Fatal(err)
			}
			if rev.Stepup != c.stepup {
				t.Fatalf("review stepup = %v, want %v (guarded %+v)", rev.Stepup, c.stepup, rev.Guarded)
			}
			var asked *bool
			if _, err := p.ApplyWith(rev.Token, func(need bool) error { asked = &need; return nil }); err != nil {
				t.Fatal(err)
			}
			if asked == nil || *asked != c.stepup {
				t.Fatalf("apply's gate was asked %v, want %v", asked, c.stepup)
			}
		})
	}
}

// A refusing gate leaves the file untouched.
func TestApplyGateRefusal(t *testing.T) {
	p, path, reloaded := newPipe(t, guardedSample, nil)
	if _, err := p.Stage(Change{Path: []string{"operators", "ops", "ssh_key"}, Value: "ssh-ed25519 AAAA attacker"}); err != nil {
		t.Fatal(err)
	}
	rev, _ := p.Review()
	refused := errors.New("needs a step-up")
	if _, err := p.ApplyWith(rev.Token, func(need bool) error {
		if need {
			return refused
		}
		return nil
	}); !errors.Is(err, refused) {
		t.Fatalf("apply = %v, want the gate's refusal", err)
	}
	if b, _ := os.ReadFile(path); string(b) != guardedSample || *reloaded {
		t.Fatal("a refused apply changed the file")
	}
}

// The sign-in-relevant part of the loaded config is compared too, so an edit
// that changes the operators only through what the file pulls in (imports, a
// pack) still needs a step-up.
func TestStepupComparesEffectiveConfig(t *testing.T) {
	path := writeCfg(t, guardedSample+"settings:\n  source: a\n")
	p := New(Options{
		Path: path,
		// The fake loader stands in for config.Load: settings.source picks
		// which operator set the loaded config ends up with.
		Guarded: func(file string) (any, error) {
			b, err := os.ReadFile(file)
			if err != nil {
				return nil, err
			}
			if strings.Contains(string(b), "source: b") {
				return "operators from b", nil
			}
			return "operators from a", nil
		},
	})
	if _, err := p.Stage(Change{Path: []string{"settings", "source"}, Value: "b"}); err != nil {
		t.Fatal(err)
	}
	rev, err := p.Review()
	if err != nil {
		t.Fatal(err)
	}
	if !rev.Stepup {
		t.Fatal("a change to the loaded operators must need a step-up")
	}
}

// When either side can't be loaded for the comparison, the answer errs towards
// asking: the edit needs a step-up.
func TestStepupWhenTheLoadedConfigCantBeCompared(t *testing.T) {
	path := writeCfg(t, guardedSample+"settings:\n  source: a\n")
	p := New(Options{
		Path: path,
		Guarded: func(file string) (any, error) {
			b, err := os.ReadFile(file)
			if err != nil {
				return nil, err
			}
			if strings.Contains(string(b), "source: b") {
				return nil, errors.New("does not load")
			}
			return "same", nil
		},
	})
	if _, err := p.Stage(Change{Path: []string{"settings", "source"}, Value: "b"}); err != nil {
		t.Fatal(err)
	}
	rev, err := p.Review()
	if err != nil {
		t.Fatal(err)
	}
	if !rev.Stepup {
		t.Fatal("a candidate that can't be loaded for the comparison must need a step-up")
	}
}
