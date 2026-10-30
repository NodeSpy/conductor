package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/dashboard/auth"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

// stepupRig is a signed-in operator on a plain-HTTP bind listener, with the
// config file the dashboard edits.
type stepupRig struct {
	s       *Server
	cfgPath string
	cfgText string
	key     sshTestKey
	c       *browserClient
}

// newStepupRig builds a dashboard whose config is base plus authExtra under
// dashboard.auth, signs ops in on the bind listener and returns the client.
func newStepupRig(t *testing.T, authExtra string) *stepupRig {
	t.Helper()
	key := newSSHTestKey(t, "ops")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgText := "connectors:\n  hooks:\n    use: webhook\n    options:\n      sources: { push: { path: /push } }\n      secret: s3cr3t\n" +
		"triggers:\n  - on: hooks.push\n    steps:\n      - command: [\"true\"]\n" +
		"pricing:\n  default: {input: 1, output: 2}\n" +
		"operators:\n  ops:\n    ssh_key: " + key.pub + "\n" +
		"dashboard:\n  enabled: true\n  bind:\n    - address: 0.0.0.0:7795\n      hostnames: [box.lan]\n      allow_plain_http: true\n"
	if authExtra != "" {
		cfgText += "  auth:\n" + authExtra
	}
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("baseline config must load: %v", err)
	}
	st, err := store.Open(store.Options{StatePath: filepath.Join(dir, "state.json"), AuditPath: filepath.Join(dir, "audit.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(Deps{
		Cfg: cfg, Store: st, Events: flow.NewEventHub(), StateDir: dir,
		ConfigPath: cfgPath, AuditPath: filepath.Join(dir, "audit.jsonl"),
		Validate:  func(p string) error { _, err := config.Load(p); return err },
		Reload:    func() error { return nil },
		Operators: func() map[string]string { return map[string]string{"ops": key.pub} },
		Log:       func(string, ...any) {},
	})
	if !s.authEnforced() {
		t.Fatal("auth must be enforced")
	}
	h := s.bindHandler(cfg.Dashboard.Bind[0])
	r := browserRequest(cfg.Dashboard.Bind[0])
	ck, _, err := s.auth.CreateSessionFor(r, "ops", auth.MethodSSH, "")
	if err != nil {
		t.Fatal(err)
	}
	c := &browserClient{t: t, h: h, host: "box.lan:7795", addr: "192.0.2.50:1111", cookie: ck}
	var state struct {
		CSRF string `json:"csrf"`
	}
	_ = json.Unmarshal(c.do("GET", "/api/v1/auth/state", nil).Body.Bytes(), &state)
	c.csrf = state.CSRF
	return &stepupRig{s: s, cfgPath: cfgPath, cfgText: cfgText, key: key, c: c}
}

func browserRequest(b config.DashboardBind) *http.Request {
	r, _ := http.NewRequest("POST", "/", nil)
	r.Host, r.RemoteAddr = "box.lan:7795", "192.0.2.50:1111"
	return auth.WithConn(r, auth.ConnInfo{Listener: bindListener(b), Transport: auth.TransportHTTP})
}

// stage stages each change and returns the review.
func (g *stepupRig) stage(t *testing.T, changes ...api.StageRequest) api.ReviewResponse {
	t.Helper()
	for _, ch := range changes {
		if rr := g.c.do("POST", "/api/v1/config/stage", ch); rr.Code != 200 {
			t.Fatalf("stage %v: %d %s", ch.Path, rr.Code, rr.Body)
		}
	}
	var rev api.ReviewResponse
	_ = json.Unmarshal(g.c.do("GET", "/api/v1/config/review", nil).Body.Bytes(), &rev)
	return rev
}

// applyRefused applies rev with no step-up and wants the 403 and an unchanged file.
func (g *stepupRig) applyRefused(t *testing.T, rev api.ReviewResponse) {
	t.Helper()
	rr := g.c.do("POST", "/api/v1/config/apply", api.ApplyRequest{Token: rev.Token})
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "stepup_required") {
		t.Fatalf("apply without a step-up: %d %s, want 403 stepup_required", rr.Code, rr.Body)
	}
	if b, _ := os.ReadFile(g.cfgPath); string(b) != g.cfgText {
		t.Fatal("a refused apply changed the config file")
	}
}

// sshStepup confirms purpose+digest with the operator's key and returns the token.
func (g *stepupRig) sshStepup(t *testing.T, digest string) string {
	t.Helper()
	var ch auth.SSHChallengeView
	rr := g.c.do("POST", "/api/v1/auth/stepup/ssh/begin", map[string]string{"purpose": string(auth.PurposeConfigApply), "digest": digest})
	if err := json.Unmarshal(rr.Body.Bytes(), &ch); err != nil || ch.ID == "" {
		t.Fatalf("ssh step-up begin: %d %s", rr.Code, rr.Body)
	}
	sig, err := auth.SignSSH(context.Background(), g.key.priv, ch.Namespace, []byte(ch.Text))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Token string `json:"token"`
	}
	rr = g.c.do("POST", "/api/v1/auth/stepup/ssh/finish", map[string]string{"challenge_id": ch.ID, "signature": sig})
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out.Token == "" {
		t.Fatalf("ssh step-up finish: %d %s", rr.Code, rr.Body)
	}
	return out.Token
}

// reviewerPayload is a cron connector that fires a trigger whose step runs a
// shell command: nothing under operators or the dashboard, no widening leaf,
// and a shell on the box all the same.
var reviewerPayload = []api.StageRequest{
	{Path: []string{"connectors", "tick"}, Value: map[string]any{"use": "cron", "schedules": map[string]any{"x": map[string]any{"every": "15s", "run_on_start": true}}}},
	{Path: []string{"triggers"}, Value: []any{map[string]any{"on": "tick.x", "steps": []any{map[string]any{"id": "pwn", "use": "cli", "command": []any{"sh", "-c", "id > /tmp/pwned"}}}}}},
}

// By default, with sign-in on, every config apply needs a step-up: a session
// cookie alone applies nothing, not even a pricing tweak, and the cron + cli
// payload is named for what it is.
func TestEveryApplyNeedsAStepupByDefault(t *testing.T) {
	g := newStepupRig(t, "")
	rev := g.stage(t, reviewerPayload...)
	if !rev.Stepup {
		t.Fatalf("the review must ask for a step-up: %+v", rev)
	}
	var guarded []string
	for _, e := range rev.Guarded {
		guarded = append(guarded, e.Path)
	}
	if strings.Join(guarded, ",") != "triggers,connectors" {
		t.Fatalf("guarded = %v, want triggers and connectors named", guarded)
	}
	g.applyRefused(t, rev)

	g.c.do("POST", "/api/v1/config/clear", nil)
	rev = g.stage(t, api.StageRequest{Path: []string{"pricing", "default", "input"}, Value: 3})
	if !rev.Stepup || len(rev.Guarded) != 0 {
		t.Fatalf("an inert edit under config_stepup: all still needs a step-up and guards nothing: %+v", rev)
	}
	g.applyRefused(t, rev)

	// With the step-up, it applies.
	tok := g.sshStepup(t, rev.Token)
	if rr := g.c.do("POST", "/api/v1/config/apply", api.ApplyRequest{Token: rev.Token, StepupToken: tok}); rr.Code != 200 {
		t.Fatalf("apply with a step-up: %d %s", rr.Code, rr.Body)
	}
	if b, _ := os.ReadFile(g.cfgPath); !strings.Contains(string(b), "input: 3") {
		t.Fatalf("the applied file: %s", b)
	}
}

// config_stepup: guarded lets an edit confined to the inert paths apply on the
// session alone; anything that runs on the box, or the dashboard and store
// blocks, still needs a step-up.
func TestGuardedConfigStepup(t *testing.T) {
	for _, c := range []struct {
		name    string
		changes []api.StageRequest
	}{
		{"the cron + cli payload", reviewerPayload},
		{"store.state_file", []api.StageRequest{{Path: []string{"store", "state_file"}, Value: "/tmp/elsewhere/state.json"}}},
		{"dashboard.port", []api.StageRequest{{Path: []string{"dashboard", "port"}, Value: 7000}}},
		{"dashboard.enabled", []api.StageRequest{{Path: []string{"dashboard", "enabled"}, Value: false}}},
		{"back to all", []api.StageRequest{{Path: []string{"dashboard", "auth", "config_stepup"}, Value: "all"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newStepupRig(t, "    config_stepup: guarded\n")
			rev := g.stage(t, c.changes...)
			if !rev.Stepup || len(rev.Guarded) == 0 {
				t.Fatalf("the review must ask for a step-up and name the guarded part: %+v", rev)
			}
			g.applyRefused(t, rev)
		})
	}
	t.Run("usage.mode with no usage block", func(t *testing.T) {
		g := newStepupRig(t, "    config_stepup: guarded\n")
		if rev := g.stage(t, api.StageRequest{Path: []string{"usage", "mode"}, Value: "api"}); rev.Stepup {
			t.Fatalf("usage.mode is inert with or without a usage block: %+v", rev)
		}
	})
	t.Run("pricing", func(t *testing.T) {
		g := newStepupRig(t, "    config_stepup: guarded\n")
		rev := g.stage(t, api.StageRequest{Path: []string{"pricing", "default", "input"}, Value: 3})
		if rev.Stepup {
			t.Fatalf("an inert edit needs no step-up under guarded: %+v", rev)
		}
		if rr := g.c.do("POST", "/api/v1/config/apply", api.ApplyRequest{Token: rev.Token}); rr.Code != 200 {
			t.Fatalf("apply: %d %s", rr.Code, rr.Body)
		}
	})
}

// If the sign-in store can't be opened, the dashboard must not serve as if no
// sign-in were needed: every listener refuses the API (503), the localhost one
// says why, and nothing can be staged or applied.
func TestAuthStoreFailureFailsClosed(t *testing.T) {
	for _, c := range []struct {
		name   string
		break_ func(dir string)
	}{
		{"dashboard is a file", func(dir string) { _ = os.WriteFile(filepath.Join(dir, "dashboard"), []byte("x"), 0o600) }},
		{"auth.json truncated", func(dir string) {
			_ = os.MkdirAll(filepath.Join(dir, "dashboard"), 0o700)
			_ = os.WriteFile(filepath.Join(dir, "dashboard", "auth.json"), []byte("{\"operators\": {"), 0o600)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			key := newSSHTestKey(t, "ops")
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "config.yaml")
			cfgText := "connectors:\n  hooks:\n    use: webhook\n    options:\n      sources: { push: { path: /push } }\n      secret: s3cr3t\n" +
				"triggers:\n  - on: hooks.push\n    steps:\n      - command: [\"true\"]\n" +
				"store:\n  state_file: " + filepath.Join(dir, "state.json") + "\n" +
				"operators:\n  ops:\n    ssh_key: " + key.pub + "\n" +
				"dashboard:\n  enabled: true\n  bind:\n    - address: 0.0.0.0:7795\n      hostnames: [box.lan]\n      allow_plain_http: true\n"
			if err := os.WriteFile(cfgPath, []byte(cfgText), 0o600); err != nil {
				t.Fatal(err)
			}
			c.break_(dir)
			cfg, err := config.Load(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			st, err := store.Open(store.Options{StatePath: filepath.Join(dir, "state.json"), AuditPath: filepath.Join(dir, "audit.jsonl")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			var logged []string
			s := New(Deps{
				Cfg: cfg, Store: st, Events: flow.NewEventHub(),
				ConfigPath: cfgPath, AuditPath: filepath.Join(dir, "audit.jsonl"),
				Validate:  func(p string) error { _, err := config.Load(p); return err },
				Reload:    func() error { return nil },
				Operators: func() map[string]string { return map[string]string{"ops": key.pub} },
				Log:       func(f string, a ...any) { logged = append(logged, f) },
			})
			if s.authErr == nil {
				t.Fatal("a broken sign-in store must be recorded")
			}
			if !strings.Contains(s.authState(), "unavailable") {
				t.Fatalf("authState = %q, want it to say sign-in is unavailable", s.authState())
			}
			bind := &browserClient{t: t, h: s.bindHandler(cfg.Dashboard.Bind[0]), host: "box.lan:7795", addr: "192.0.2.66:1"}
			local := &browserClient{t: t, h: s.listenerHandler(auth.ListenerLocalhost), host: "localhost:7777", addr: "127.0.0.1:1"}
			for _, p := range []string{"/api/v1/config", "/api/v1/auth/state", "/api/v1/runs"} {
				if rr := bind.do("GET", p, nil); rr.Code != http.StatusServiceUnavailable || strings.Contains(rr.Body.String(), dir) {
					t.Fatalf("bind GET %s: %d %s, want 503 without the path", p, rr.Code, rr.Body)
				}
			}
			if rr := bind.do("POST", "/api/v1/config/stage", api.StageRequest{Path: []string{"operators", "mallory"}, Value: map[string]any{"ssh_key": key.pub}}); rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("bind stage: %d %s", rr.Code, rr.Body)
			}
			rr := local.do("GET", "/api/v1/config", nil)
			if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "sign-in store") {
				t.Fatalf("localhost GET: %d %s, want 503 naming the cause", rr.Code, rr.Body)
			}
			if b, _ := os.ReadFile(cfgPath); string(b) != cfgText {
				t.Fatal("the config changed")
			}
			if len(logged) == 0 {
				t.Fatal("the cause must be logged")
			}
		})
	}
}

// The audit attributes an apply and the step-up that confirmed it: operator,
// session, sign-in method, address, listener and the digest, so a change can be
// tied to the session and network that made it.
func TestApplyAuditNamesWhoAndWhere(t *testing.T) {
	g := newStepupRig(t, "")
	rev := g.stage(t, api.StageRequest{Path: []string{"pricing", "default", "input"}, Value: 3})
	tok := g.sshStepup(t, rev.Token)
	if rr := g.c.do("POST", "/api/v1/config/apply", api.ApplyRequest{Token: rev.Token, StepupToken: tok}); rr.Code != 200 {
		t.Fatalf("apply: %d %s", rr.Code, rr.Body)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(g.cfgPath), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) == nil {
			seen[e["event"].(string)] = e
		}
	}
	for _, ev := range []string{"dashboard.config.apply", "dashboard_stepup"} {
		e := seen[ev]
		if e == nil {
			t.Fatalf("no %s entry in %s", ev, b)
		}
		for k, want := range map[string]string{"operator": "ops", "method": "ssh", "addr": "192.0.2.50:1111",
			"listener": string(bindListener(g.s.deps.Cfg.Dashboard.Bind[0])), "digest": rev.Token} {
			if e[k] != want {
				t.Errorf("%s %s = %v, want %q", ev, k, e[k], want)
			}
		}
		if s, _ := e["session"].(string); s == "" {
			t.Errorf("%s names no session: %v", ev, e)
		}
	}
	if seen["dashboard.config.apply"]["session"] != seen["dashboard_stepup"]["session"] {
		t.Fatalf("the apply and its step-up must name the same session: %v %v", seen["dashboard.config.apply"], seen["dashboard_stepup"])
	}
}

// A staged ssh_key carrying terminal escapes fails validation, and an apply
// with a valid step-up still refuses it: the file is unchanged.
func TestStagedSSHKeyWithControlsIsRefused(t *testing.T) {
	g := newStepupRig(t, "")
	rev := g.stage(t, api.StageRequest{Path: []string{"operators", "ops", "ssh_key"},
		Value: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIkVUk x\r\x1b[2K\x1b[1A  action:   wording only\x1b[K"})
	if rr := g.c.do("GET", "/api/v1/config/validate", nil); !strings.Contains(rr.Body.String(), "ssh_key contains a carriage return") {
		t.Fatalf("validate must refuse the key: %s", rr.Body)
	}
	tok := g.sshStepup(t, rev.Token)
	rr := g.c.do("POST", "/api/v1/config/apply", api.ApplyRequest{Token: rev.Token, StepupToken: tok})
	if rr.Code == http.StatusOK || !strings.Contains(rr.Body.String(), "ssh_key contains a carriage return") {
		t.Fatalf("apply must refuse the key: %d %s", rr.Code, rr.Body)
	}
	if b, _ := os.ReadFile(g.cfgPath); string(b) != g.cfgText {
		t.Fatal("a refused apply changed the config file")
	}
}

// With config_stepup: guarded, lowering a price is a widening: the hard
// max_cost_usd budget caps count with pricing.
func TestGuardedLoweringAPriceNeedsAStepup(t *testing.T) {
	g := newStepupRig(t, "    config_stepup: guarded\n")
	rev := g.stage(t, api.StageRequest{Path: []string{"pricing", "default", "input"}, Value: 0})
	if !rev.Stepup || len(rev.Widening) == 0 || !strings.Contains(rev.Description, "pricing.default.input: 1 → 0") {
		t.Fatalf("a lowered price must ask for a step-up and say why: %+v", rev)
	}
	g.applyRefused(t, rev)
}

// Applies that skip the step-up are rate-limited: past quietApplyBurst in the
// window, the review asks for one and the apply refuses without it.
func TestQuietAppliesAreRateLimited(t *testing.T) {
	g := newStepupRig(t, "    config_stepup: guarded\n")
	for i := 0; i < quietApplyBurst; i++ {
		rev := g.stage(t, api.StageRequest{Path: []string{"pricing", "default", "output"}, Value: 3 + i})
		if rev.Stepup {
			t.Fatalf("apply %d: %+v", i, rev)
		}
		if rr := g.c.do("POST", "/api/v1/config/apply", api.ApplyRequest{Token: rev.Token}); rr.Code != 200 {
			t.Fatalf("apply %d: %d %s", i, rr.Code, rr.Body)
		}
	}
	b, _ := os.ReadFile(g.cfgPath)
	g.cfgText = string(b)
	// Every apply restarts the daemon: a new server over the same state
	// still has the window spent.
	if !New(g.s.deps).quiet.spent() {
		t.Fatal("a restart reset the quiet-apply window")
	}
	rev := g.stage(t, api.StageRequest{Path: []string{"pricing", "default", "output"}, Value: 100})
	if !rev.Stepup {
		t.Fatalf("past the burst the review must ask for a step-up: %+v", rev)
	}
	g.applyRefused(t, rev)
	tok := g.sshStepup(t, rev.Token)
	if rr := g.c.do("POST", "/api/v1/config/apply", api.ApplyRequest{Token: rev.Token, StepupToken: tok}); rr.Code != 200 {
		t.Fatalf("a confirmed apply still goes through: %d %s", rr.Code, rr.Body)
	}
}

// The quiet-apply window is kept on disk. A damaged record counts as spent,
// and a use that can't be kept isn't granted.
func TestQuietApplyWindowIsKept(t *testing.T) {
	dir := t.TempDir()
	clk := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clk }
	path := filepath.Join(dir, "quiet-applies.json")
	b := newApplyBudget(now, path)
	for i := 0; i < quietApplyBurst-1; i++ {
		if !b.take() {
			t.Fatalf("use %d refused", i)
		}
	}
	b = newApplyBudget(now, path)
	if !b.take() || b.take() {
		t.Fatal("the window must carry over a restart: one use left, then none")
	}
	clk = clk.Add(quietApplyWindow + time.Second)
	if newApplyBudget(now, path).spent() {
		t.Fatal("the window moves on")
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !newApplyBudget(now, path).spent() {
		t.Fatal("a damaged record must count as a spent window")
	}
	blocked := filepath.Join(dir, "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if newApplyBudget(now, filepath.Join(blocked, "quiet-applies.json")).take() {
		t.Fatal("a use that can't be kept must not be granted")
	}
}

// Staged edits are per session: another session's edits don't show in this
// one's config read or review, and its review token can't apply them.
func TestStagedEditsArePerSession(t *testing.T) {
	g := newStepupRig(t, "")
	r := browserRequest(g.s.deps.Cfg.Dashboard.Bind[0])
	ck, _, err := g.s.auth.CreateSessionFor(r, "ops", auth.MethodSSH, "")
	if err != nil {
		t.Fatal(err)
	}
	other := &browserClient{t: t, h: g.c.h, host: g.c.host, addr: "192.0.2.77:1", cookie: ck}
	var st struct{ CSRF string }
	_ = json.Unmarshal(other.do("GET", "/api/v1/auth/state", nil).Body.Bytes(), &st)
	other.csrf = st.CSRF

	if rr := other.do("POST", "/api/v1/config/stage", api.StageRequest{Path: []string{"operators", "ops", "ssh_key"}, Value: "ssh-ed25519 AAAA attacker", Summary: "update sre description"}); rr.Code != 200 {
		t.Fatal(rr.Body)
	}
	var cfg api.ConfigResponse
	_ = json.Unmarshal(g.c.do("GET", "/api/v1/config", nil).Body.Bytes(), &cfg)
	if len(cfg.Staged) != 0 {
		t.Fatalf("this session sees another's staged edits: %+v", cfg.Staged)
	}
	rev := g.stage(t, api.StageRequest{Path: []string{"pricing", "default", "input"}, Value: 5})
	if strings.Contains(rev.Description, "attacker") || strings.Contains(rev.Unified, "attacker") {
		t.Fatalf("another session's edit rode into this review:\n%s", rev.Description)
	}
	var theirs api.ReviewResponse
	_ = json.Unmarshal(other.do("GET", "/api/v1/config/review", nil).Body.Bytes(), &theirs)
	if _, refused := g.pendingFor(t, auth.PurposeConfigApply, theirs.Token); refused == "" {
		t.Fatal("this session could begin an SSH step-up over another session's review")
	}
	if rr := g.c.do("POST", "/api/v1/config/apply", api.ApplyRequest{Token: theirs.Token}); rr.Code != http.StatusConflict {
		t.Fatalf("another session's review token applied here: %d %s", rr.Code, rr.Body)
	}
	if b, _ := os.ReadFile(g.cfgPath); strings.Contains(string(b), "attacker") {
		t.Fatal("the other session's edit was written")
	}
}
