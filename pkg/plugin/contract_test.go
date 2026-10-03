package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// An unknown semantic refuses the plugin, at every level and one level down;
// listing it in optional lets an older host ignore it.
func TestCheckSemanticsIsMustUnderstand(t *testing.T) {
	cases := []struct {
		name string
		decl string
		want []string // substrings, one per expected problem
	}{
		{"all known", `{"semantics":{"poll":{}},"events":[{"name":"e","semantics":{"cursor":{"id":"n"},"bound_to_target":true}}],"verbs":[{"name":"v","semantics":{"host_only":true}}]}`, nil},
		{"unknown event key", `{"events":[{"name":"e","semantics":{"teleport":true}}]}`, []string{"events[e].semantics.teleport"}},
		{"unknown verb key", `{"verbs":[{"name":"v","semantics":{"teleport":true}}]}`, []string{"verbs[v].semantics.teleport"}},
		{"unknown connection key", `{"semantics":{"teleport":{}}}`, []string{"semantics.teleport"}},
		{"unknown nested field", `{"events":[{"name":"e","semantics":{"cursor":{"id":"n","ordering":"desc"}}}]}`, []string{`unknown field "ordering"`}},
		{"optional unknown key", `{"events":[{"name":"e","semantics":{"teleport":true,"optional":["teleport"]}}]}`, nil},
		{"optional does not excuse a bad known key", `{"events":[{"name":"e","semantics":{"cursor":{"x":1},"optional":["cursor"]}}]}`, []string{`unknown field "x"`}},
		{"bad completion", `{"events":[{"name":"e","semantics":{"completion":"sometimes"}}]}`, []string{"completion must be edge or level"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CheckSemantics([]byte(c.decl))
			if len(got) != len(c.want) {
				t.Fatalf("problems = %q, want %d matching %q", got, len(c.want), c.want)
			}
			for i, w := range c.want {
				if !strings.Contains(got[i], w) {
					t.Fatalf("problem %d = %q, want it to mention %q", i, got[i], w)
				}
			}
		})
	}
}

func TestCompletionWireForms(t *testing.T) {
	for in, level := range map[string]bool{`"edge"`: false, `"level"`: true, `{"level":{"rearm_on":"revision"}}`: true, `{"edge":{}}`: false} {
		var c Completion
		if err := json.Unmarshal([]byte(in), &c); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if (c.Level != nil) != level {
			t.Fatalf("%s: level = %v", in, c.Level != nil)
		}
	}
	b, _ := json.Marshal(Completion{Level: &LevelCompletion{RearmOn: "revision"}})
	if string(b) != `{"level":{"rearm_on":"revision"}}` {
		t.Fatalf("marshal = %s", b)
	}
	if err := json.Unmarshal([]byte(`{"level":{"rearm_on":"tuesday"}}`), new(Completion)); err == nil {
		t.Fatal("rearm_on must be revision")
	}
}

// Declarations must hang together: every verb a semantic names exists and
// carries the semantic that fits.
func TestValidateSemanticsReferences(t *testing.T) {
	good := Decl{
		Verbs: []Verb{
			{Name: "get_run"}, {Name: "rerun"},
			{Name: "mint", Semantics: &VerbSemantics{HostOnly: true, MintsCredential: &MintsCredential{Credential: "w"}}},
			{Name: "open", Semantics: &VerbSemantics{HostOnly: true, Exposes: &Exposes{Local: "addr", URL: "url", Release: "close"}}},
			{Name: "close"},
		},
		Events: []Event{{Name: "fail", Semantics: &EventSemantics{Remediate: &RemediateSemantics{
			Option: "flaky", Run: "run_id", Status: RemediateCheck{Verb: "get_run", DoneWhen: "status == 'completed'"}, Action: RemediateVerb{Verb: "rerun"}}}}},
		Semantics: &ConnSemantics{Credentials: []Credential{{Name: "w", Role: "write", Mint: CredentialMint{Verb: "mint"}}}},
	}
	if p := ValidateSemantics(good); len(p) != 0 {
		t.Fatalf("valid decl reported %q", p)
	}

	bad := good
	bad.Verbs = []Verb{{Name: "mint", Semantics: &VerbSemantics{MintsCredential: &MintsCredential{Credential: "other"}}}}
	p := strings.Join(ValidateSemantics(bad), "\n")
	for _, want := range []string{
		`names verb "get_run"`, `names verb "rerun"`, // remediate verbs missing
		"must be host_only",             // a credential minter outside host_only
		"must declare mints_credential", // credential names a verb minting another one
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("problems missing %q:\n%s", want, p)
		}
	}
}

// option_hooks: a verb it names must exist, its `at` must be a known phase,
// and the same option may not be mapped twice.
func TestValidateSemanticsOptionHooks(t *testing.T) {
	good := Decl{
		Verbs: []Verb{{Name: "feedback"}},
		Events: []Event{{Name: "e", Semantics: &EventSemantics{OptionHooks: []OptionHook{
			{Option: "ack", At: "start", Verb: "feedback"},
			{Option: "on_done", At: "done", Verb: "feedback"},
			{Option: "on_fail", At: "fail", Verb: "feedback"},
		}}}},
	}
	if p := ValidateSemantics(good); len(p) != 0 {
		t.Fatalf("valid decl reported %q", p)
	}

	cases := []struct {
		name string
		oh   OptionHook
		want string
	}{
		{"unknown verb", OptionHook{Option: "ack", At: "start", Verb: "nope"}, `names verb "nope"`},
		{"bad at", OptionHook{Option: "ack", At: "sometimes", Verb: "feedback"}, "must be start, done or fail"},
		{"missing fields", OptionHook{}, "option, at and verb are required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Decl{Verbs: []Verb{{Name: "feedback"}}, Events: []Event{{Name: "e", Semantics: &EventSemantics{OptionHooks: []OptionHook{c.oh}}}}}
			p := strings.Join(ValidateSemantics(d), "\n")
			if !strings.Contains(p, c.want) {
				t.Fatalf("problems missing %q:\n%s", c.want, p)
			}
		})
	}

	t.Run("duplicate option", func(t *testing.T) {
		d := Decl{Verbs: []Verb{{Name: "feedback"}}, Events: []Event{{Name: "e", Semantics: &EventSemantics{OptionHooks: []OptionHook{
			{Option: "ack", At: "start", Verb: "feedback"},
			{Option: "ack", At: "done", Verb: "feedback"},
		}}}}}
		p := strings.Join(ValidateSemantics(d), "\n")
		if !strings.Contains(p, `option "ack" is mapped more than once`) {
			t.Fatalf("want a duplicate-option problem, got %q", p)
		}
	})
}

// An exposes verb that is not host_only lets a flow step or agent tunnel an
// arbitrary local address out to the public internet — it must be refused
// exactly like a mints_credential verb that isn't host_only.
func TestValidateSemanticsExposesMustBeHostOnly(t *testing.T) {
	d := Decl{Verbs: []Verb{
		{Name: "open", Semantics: &VerbSemantics{Exposes: &Exposes{Local: "addr", URL: "url"}}},
	}}
	p := strings.Join(ValidateSemantics(d), "\n")
	if !strings.Contains(p, "must be host_only") {
		t.Fatalf("an exposes verb without host_only must be refused, got %q", p)
	}
}

// exposes.path (plugin-contract.md §2.3), when declared, names an OPTION the
// verb itself accepts a listener's resolved path in — the same "must exist"
// treatment local/url/release already imply by being the verb's own
// option/output names. A typo (naming an option the verb never declares)
// would otherwise be a silent no-op: the host would pass it, and the plugin
// would just never see it.
func TestValidateSemanticsExposesPathMustNameADeclaredOption(t *testing.T) {
	bad := Decl{Verbs: []Verb{
		{Name: "open", Options: Schema{"local_addr": {Type: "string"}},
			Semantics: &VerbSemantics{HostOnly: true, Exposes: &Exposes{Local: "local_addr", URL: "public_url", Path: "route"}}},
	}}
	p := strings.Join(ValidateSemantics(bad), "\n")
	if !strings.Contains(p, `names option "route"`) {
		t.Fatalf("exposes.path naming an option the verb never declares must be refused, got %q", p)
	}

	good := Decl{Verbs: []Verb{
		{Name: "open", Options: Schema{"local_addr": {Type: "string"}, "path": {Type: "string"}},
			Semantics: &VerbSemantics{HostOnly: true, Exposes: &Exposes{Local: "local_addr", URL: "public_url", Path: "path"}}},
	}}
	if p := ValidateSemantics(good); len(p) != 0 {
		t.Fatalf("exposes.path naming a real option reported %q", p)
	}
}

// TestValidateSemanticsListenerFieldsMustNameDeclaredConnectionFields is
// finding 5: Listener.Listen/Expose/URLTo/Path named config fields the
// connection schema never declared went unchecked — a typo in any of them
// silently resolved to nothing at instance start (an empty listen address,
// no exposure opened, no public URL ever filled in) with no refusal at
// describe time, the same class of gap TestValidateSemanticsExposesPath
// MustNameADeclaredOption above closes for exposes.path.
func TestValidateSemanticsListenerFieldsMustNameDeclaredConnectionFields(t *testing.T) {
	for _, tc := range []struct {
		name  string
		l     Listener
		field string
		value string
	}{
		{"listen", Listener{Listen: "nope"}, "listen", "nope"},
		{"expose", Listener{Listen: "listen", Expose: "nope"}, "expose", "nope"},
		{"url_to", Listener{Listen: "listen", URLTo: "nope"}, "url_to", "nope"},
		{"path", Listener{Listen: "listen", Path: "nope"}, "path", "nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Decl{
				Connection: Schema{"listen": {Type: "string"}},
				Semantics:  &ConnSemantics{Listeners: []Listener{tc.l}},
			}
			p := strings.Join(ValidateSemantics(d), "\n")
			if !strings.Contains(p, fmt.Sprintf("%s: %q does not name a declared connection field", tc.field, tc.value)) {
				t.Fatalf("listener.%s naming an undeclared connection field must be refused, got %q", tc.field, p)
			}
		})
	}

	// The positive case: every field present and declared (acme-listener's
	// actual shape) passes clean.
	good := Decl{
		Connection: Schema{
			"listen": {Type: "string"}, "expose": {Type: "string"},
			"public_url": {Type: "string"}, "path": {Type: "string"},
		},
		Semantics: &ConnSemantics{Listeners: []Listener{
			{Listen: "listen", Expose: "expose", URLTo: "public_url", Path: "path"},
		}},
	}
	if p := ValidateSemantics(good); len(p) != 0 {
		t.Fatalf("a listener naming only declared connection fields reported %q", p)
	}

	// The dotted/nested case (coretest's forge-decl.json, the github
	// plugin's real shape): only the FIRST segment is checkable — a nested
	// object field (Type "map") is opaque beyond its own name.
	nested := Decl{
		Connection: Schema{"webhook": {Type: "map"}},
		Semantics: &ConnSemantics{Listeners: []Listener{
			{Listen: "webhook.listen", Expose: "webhook.expose", URLTo: "webhook.public_url", Path: "webhook.path"},
		}},
	}
	if p := ValidateSemantics(nested); len(p) != 0 {
		t.Fatalf("a dotted listener field whose FIRST segment is declared must pass, got %q", p)
	}
	nestedBad := Decl{
		Connection: Schema{"webhook": {Type: "map"}},
		Semantics: &ConnSemantics{Listeners: []Listener{
			{Listen: "webhook.listen", Expose: "wrongtop.expose"},
		}},
	}
	p := strings.Join(ValidateSemantics(nestedBad), "\n")
	if !strings.Contains(p, `"wrongtop.expose" does not name a declared connection field`) {
		t.Fatalf("a dotted listener field whose FIRST segment is undeclared must be refused, got %q", p)
	}
}

// Serve drops the OPTIONAL semantics a host does not implement before
// replying, and keeps everything else.
func TestDescribeStripsOptionalSemanticsTheHostLacks(t *testing.T) {
	h := ConnectorFunc(func() Decl {
		return Decl{Type: "t", Events: []Event{{Name: "e", Semantics: &EventSemantics{
			Cursor: &CursorSemantics{ID: "n"}, Feedback: true, Optional: []string{"feedback"},
		}}}}
	}, nil)
	resp := roundTrip(t, h, `{"jsonrpc":"2.0","id":1,"method":"plugin.describe","params":{"host":{"semantics":["event.cursor"]}}}`)
	var d Decl
	if err := json.Unmarshal(resp["1"].Result, &d); err != nil {
		t.Fatal(err)
	}
	s := d.Events[0].Semantics
	if s.Cursor == nil || s.Feedback {
		t.Fatalf("semantics after strip = %+v, want cursor kept and feedback dropped", s)
	}

	// No host info (an older daemon): nothing is stripped.
	resp = roundTrip(t, h, `{"jsonrpc":"2.0","id":1,"method":"plugin.describe"}`)
	_ = json.Unmarshal(resp["1"].Result, &d)
	if !d.Events[0].Semantics.Feedback {
		t.Fatal("describe without host info must not strip anything")
	}
}

type fullPlugin struct {
	hostc chan *HostConn
}

func (fullPlugin) Describe() Decl                             { return Decl{Type: "full"} }
func (fullPlugin) Invoke(InvokeRequest) (InvokeResult, error) { return InvokeResult{}, nil }
func (fullPlugin) Poll(_ context.Context, r PollRequest) (PollResult, error) {
	if r.Mode == PollTarget {
		return PollResult{Events: []SourceEvent{{Event: "e", Target: Target{Key: r.Target}}}}, nil
	}
	return PollResult{}, nil
}
func (fullPlugin) Translate(_ context.Context, r TranslateRequest) (TranslateResult, error) {
	return TranslateResult{Events: []SourceEvent{{Event: r.Headers["X-Event"]}}}, nil
}
func (fullPlugin) Validate(_ context.Context, r ValidateRequest) (ValidateResult, error) {
	if r.Config["secret"] == nil {
		return ValidateResult{Problems: []Problem{{Path: "secret", Message: "required"}}}, nil
	}
	return ValidateResult{}, nil
}
func (fullPlugin) Stop(context.Context, StopRequest) error {
	return Fail(CodeTargetGone, "gone", map[string]any{"why": "test"})
}
func (p *fullPlugin) SetHost(h *HostConn) {
	if p.hostc != nil {
		p.hostc <- h
	}
}

func TestOptionalMethodsDispatch(t *testing.T) {
	resp := roundTrip(t, &fullPlugin{}, strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"plugin.poll","params":{"instance":"a","mode":"target","target":"x#1"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"plugin.translate","params":{"instance":"a","headers":{"X-Event":"push"},"body":"{}"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"plugin.validate","params":{"instance":"a","config":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"plugin.stop","params":{"instance":"a"}}`,
	}, "\n"))
	var poll PollResult
	_ = json.Unmarshal(resp["1"].Result, &poll)
	if len(poll.Events) != 1 || poll.Events[0].Target.Key != "x#1" {
		t.Fatalf("poll = %s", resp["1"].Result)
	}
	var tr TranslateResult
	_ = json.Unmarshal(resp["2"].Result, &tr)
	if len(tr.Events) != 1 || tr.Events[0].Event != "push" {
		t.Fatalf("translate = %s", resp["2"].Result)
	}
	var v ValidateResult
	_ = json.Unmarshal(resp["3"].Result, &v)
	if len(v.Problems) != 1 || v.Problems[0].Path != "secret" {
		t.Fatalf("validate = %s", resp["3"].Result)
	}
	if e := resp["4"].Error; e == nil || e.Code != CodeTargetGone || e.Data["why"] != "test" {
		t.Fatalf("stop error = %+v, want the contract code with its data on the wire", e)
	}

	// A plugin implementing none of them answers method-not-found to each:
	// the one negotiation.
	plain := ConnectorFunc(func() Decl { return Decl{Type: "p"} }, nil)
	resp = roundTrip(t, plain, strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"plugin.poll","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"plugin.translate","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"plugin.validate","params":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"plugin.stop","params":{}}`,
	}, "\n"))
	for _, id := range []string{"1", "2", "3", "4"} {
		if e := resp[id].Error; e == nil || e.Code != CodeMethodNotFound {
			t.Fatalf("id %s: error = %+v, want method-not-found", id, e)
		}
	}
}

// host.state goes out as a plugin→host request over the same connection and
// its answer comes back to the caller.
func TestHostStateRoundTrip(t *testing.T) {
	p := &fullPlugin{hostc: make(chan *HostConn, 1)}
	toPlugin, hostW := io.Pipe()
	hostR, fromPlugin := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeConn(toPlugin, fromPlugin, p) }()
	var host *HostConn
	select {
	case host = <-p.hostc:
	case <-time.After(5 * time.Second):
		t.Fatal("SetHost was never called")
	}

	// Play the host: answer one host.state request.
	go func() {
		sc := bufio.NewScanner(hostR)
		for sc.Scan() {
			var m wireMessage
			_ = json.Unmarshal(sc.Bytes(), &m)
			if m.Method != MethodHostState {
				continue
			}
			var req HostStateRequest
			_ = json.Unmarshal(m.Params, &req)
			res, _ := json.Marshal(HostStateResult{OK: true, Value: req.Instance + "/" + req.Key})
			b, _ := json.Marshal(wireMessage{JSONRPC: "2.0", ID: m.ID, Result: res})
			_, _ = hostW.Write(append(b, '\n'))
		}
	}()
	v, err := host.State("gh").Get(context.Background(), "claims/7")
	if err != nil || v != "gh/claims/7" {
		t.Fatalf("State.Get = %v, %v", v, err)
	}
	_ = hostW.Close()
	<-done
}

// host.auth goes out as a plugin→host request over the same connection, and
// Refresh rides the wire — the "the upstream just said 401" signal.
func TestHostAuthRoundTrip(t *testing.T) {
	p := &fullPlugin{hostc: make(chan *HostConn, 1)}
	toPlugin, hostW := io.Pipe()
	hostR, fromPlugin := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeConn(toPlugin, fromPlugin, p) }()
	var host *HostConn
	select {
	case host = <-p.hostc:
	case <-time.After(5 * time.Second):
		t.Fatal("SetHost was never called")
	}

	// Play the host: answer host.auth, echoing whether refresh was asked for
	// so the test can see it crossed the wire.
	go func() {
		sc := bufio.NewScanner(hostR)
		for sc.Scan() {
			var m wireMessage
			_ = json.Unmarshal(sc.Bytes(), &m)
			if m.Method != MethodHostAuth {
				continue
			}
			var req HostAuthRequest
			_ = json.Unmarshal(m.Params, &req)
			tok := "tok-" + req.Instance
			if req.Refresh {
				tok = "refreshed-" + req.Instance
			}
			res, _ := json.Marshal(HostAuthResult{OK: true, Token: tok})
			b, _ := json.Marshal(wireMessage{JSONRPC: "2.0", ID: m.ID, Result: res})
			_, _ = hostW.Write(append(b, '\n'))
		}
	}()
	tok, err := host.Auth("gh").Token(context.Background(), false)
	if err != nil || tok != "tok-gh" {
		t.Fatalf("Auth.Token = %v, %v", tok, err)
	}
	tok, err = host.Auth("gh").Token(context.Background(), true)
	if err != nil || tok != "refreshed-gh" {
		t.Fatalf("Auth.Token(refresh) = %v, %v", tok, err)
	}
	_ = hostW.Close()
	<-done
}

func roundTrip(t *testing.T, h Handler, lines string) map[string]wireMessage {
	t.Helper()
	var out strings.Builder
	if err := serve(strings.NewReader(lines+"\n"), &out, h); err != nil {
		t.Fatalf("serve: %v", err)
	}
	byID := map[string]wireMessage{}
	dec := json.NewDecoder(strings.NewReader(out.String()))
	for {
		var m wireMessage
		if dec.Decode(&m) != nil {
			break
		}
		if m.ID != nil {
			byID[string(*m.ID)] = m
		}
	}
	return byID
}
