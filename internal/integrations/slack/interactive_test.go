package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
)

// fakeSlackAPI records every Web API call the integration makes.
type fakeSlackAPI struct {
	mu    sync.Mutex
	calls []slackCall
}

type slackCall struct {
	method string
	body   map[string]any
}

func (f *fakeSlackAPI) byMethod(m string) []slackCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []slackCall
	for _, c := range f.calls {
		if c.method == m {
			out = append(out, c)
		}
	}
	return out
}

func startFakeSlack(t *testing.T) *fakeSlackAPI {
	t.Helper()
	f := &fakeSlackAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-1" {
			t.Errorf("%s: bot token not sent", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.calls = append(f.calls, slackCall{method: strings.TrimPrefix(r.URL.Path, "/"), body: body})
		f.mu.Unlock()
		fmt.Fprint(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	saved := []*string{&viewsOpenURL, &chatPostEphemeralURL, &chatPostMessageURL, &reactionsAddURL}
	old := make([]string, len(saved))
	for i, p := range saved {
		old[i] = *p
	}
	viewsOpenURL = srv.URL + "/views.open"
	chatPostEphemeralURL = srv.URL + "/chat.postEphemeral"
	chatPostMessageURL = srv.URL + "/chat.postMessage"
	reactionsAddURL = srv.URL + "/reactions.add"
	t.Cleanup(func() {
		for i, p := range saved {
			*p = old[i]
		}
	})
	return f
}

func handoverForm() *Form {
	return &Form{Title: "Hand off", Submit: "Start", Fields: []FormField{
		{Name: "repo", Label: "Repository", Type: "select", Options: []string{"acme/api", "acme/web"}},
		{Name: "mode", Type: "select", Options: []string{"plan", "default"}, Default: "plan"},
		{Name: "notes", Type: "textarea", Optional: true},
	}}
}

func formCfg(users []string, anyUser bool) Config {
	return Config{
		AppToken: "xapp-1", BotToken: "xoxb-1",
		Rules: []Rule{
			{On: "message_shortcut", CallbackID: "conductor_handover", Form: handoverForm(), Users: users, AnyUser: anyUser, Key: "0:slack.message_shortcut",
				Filter:  &config.Filter{Op: config.FilterOpMatch, Key: "callback_id", Val: "conductor_handover"},
				Actions: config.ActionSet{{FlowRef: "0:slack.message_shortcut"}}},
			{On: "app_mention", Form: handoverForm(), Users: users, AnyUser: anyUser, Key: "1:slack.app_mention",
				Actions: config.ActionSet{{FlowRef: "1:slack.app_mention"}}},
		},
	}
}

func shortcutPayload(user string) json.RawMessage {
	return json.RawMessage(`{"type":"message_action","callback_id":"conductor_handover","trigger_id":"trig-1",
		"user":{"id":"` + user + `"},"channel":{"id":"C1"},
		"message":{"ts":"10.1","thread_ts":"9.0","text":"the login page 500s","files":[{"id":"F1","name":"shot.png","mimetype":"image/png"}]}}`)
}

// interact runs one interactive payload the way the socket loop does:
// decide, (ACK), then the follow-up work.
func interact(g *Integration, raw json.RawMessage) (any, []string) {
	emit, got := collect()
	ack, after := g.handleInteractive(context.Background(), emit, raw)
	if after != nil {
		after()
	}
	var kinds []string
	for _, tr := range *got {
		kinds = append(kinds, tr.Kind)
	}
	return ack, kinds
}

func submission(meta string, values string) json.RawMessage {
	b, _ := json.Marshal(meta)
	return json.RawMessage(`{"type":"view_submission","user":{"id":"U1"},
		"view":{"id":"V1","callback_id":"conductor_form","private_metadata":` + string(b) + `,
		"state":{"values":` + values + `}}}`)
}

const goodValues = `{"repo":{"value":{"type":"static_select","selected_option":{"value":"acme/api"}}},
	"mode":{"value":{"type":"static_select","selected_option":{"value":"default"}}},
	"notes":{"value":{"type":"plain_text_input","value":"look at auth"}}}`

func TestShortcutOpensFormThenSubmissionFires(t *testing.T) {
	api := startFakeSlack(t)
	g := newTest(t, formCfg([]string{"U1"}, false))

	if _, kinds := interact(g, shortcutPayload("U1")); len(kinds) != 0 {
		t.Fatalf("a form shortcut must not fire before submission, got %v", kinds)
	}
	opens := api.byMethod("views.open")
	if len(opens) != 1 || opens[0].body["trigger_id"] != "trig-1" {
		t.Fatalf("views.open: %+v", opens)
	}
	view, _ := opens[0].body["view"].(map[string]any)
	if view["callback_id"] != formViewCallbackID || len(view["blocks"].([]any)) != 3 {
		t.Fatalf("modal: %+v", view)
	}
	meta, _ := view["private_metadata"].(string)
	var m formMeta
	if err := json.Unmarshal([]byte(meta), &m); err != nil {
		t.Fatal(err)
	}
	if m.Channel != "C1" || m.TS != "10.1" || m.ThreadTS != "9.0" || m.User != "U1" || m.Via != "shortcut" {
		t.Fatalf("private_metadata: %+v", m)
	}

	emit, got := collect()
	ack, after := g.handleInteractive(context.Background(), emit, submission(meta, goodValues))
	if ack != nil {
		t.Fatalf("a valid submission ACKs bare (closes the modal), got %v", ack)
	}
	after()
	if len(*got) != 1 {
		t.Fatalf("want one trigger, got %d", len(*got))
	}
	tr := (*got)[0]
	sc := tr.Context["slack"].(map[string]any)
	form := sc["form"].(map[string]any)
	if tr.Kind != "message_shortcut" || sc["via"] != "shortcut" || sc["ts"] != "10.1" || sc["thread_ts"] != "9.0" ||
		sc["user"] != "U1" || sc["callback_id"] != "conductor_handover" {
		t.Fatalf("context: %+v", sc)
	}
	if form["repo"] != "acme/api" || form["mode"] != "default" || form["notes"] != "look at auth" {
		t.Fatalf("form: %+v", form)
	}
	if files := sc["files"].([]any); len(files) != 1 {
		t.Fatalf("files: %+v", files)
	}
	if a, ok := tr.Action.(config.Action); !ok || a.FlowRef != "0:slack.message_shortcut" || a.Checkout != "none" {
		t.Fatalf("action: %+v", tr.Action)
	}
}

func TestSubmissionRejectsInvalidSelect(t *testing.T) {
	startFakeSlack(t)
	g := newTest(t, formCfg([]string{"U1"}, false))
	meta := formMeta{Key: "0:slack.message_shortcut", Channel: "C1", TS: "10.1", User: "U1", Via: "shortcut", Callback: "conductor_handover"}.encode()
	bad := `{"repo":{"value":{"type":"static_select","selected_option":{"value":"evil/repo"}}},
		"mode":{"value":{"type":"static_select","selected_option":{"value":"plan"}}}}`
	ack, kinds := interact(g, submission(meta, bad))
	if len(kinds) != 0 {
		t.Fatal("an invalid select must not fire")
	}
	a, _ := ack.(map[string]any)
	errs, _ := a["errors"].(map[string]string)
	if a["response_action"] != "errors" || errs["repo"] == "" {
		t.Fatalf("ack: %+v", ack)
	}
	// A missing required field is refused the same way.
	ack, kinds = interact(g, submission(meta, `{}`))
	if len(kinds) != 0 || ack == nil {
		t.Fatalf("missing required: ack=%v kinds=%v", ack, kinds)
	}
}

func TestShortcutUsersEnforced(t *testing.T) {
	api := startFakeSlack(t)
	g := newTest(t, formCfg([]string{"U1"}, false))
	interact(g, shortcutPayload("U2"))
	if n := len(api.byMethod("views.open")); n != 0 {
		t.Fatalf("a user outside users: must not get the form (views.open x%d)", n)
	}
	// A submission whose submitter differs from the user it was opened for
	// is ignored.
	meta := formMeta{Key: "0:slack.message_shortcut", Channel: "C1", TS: "10.1", User: "U2", Via: "shortcut"}.encode()
	if _, kinds := interact(g, submission(meta, goodValues)); len(kinds) != 0 {
		t.Fatal("a submission by a different user must not fire")
	}
	// No users and no any_user: the integration refuses on its own too.
	g2 := newTest(t, formCfg(nil, false))
	interact(g2, shortcutPayload("U1"))
	if n := len(api.byMethod("views.open")); n != 0 {
		t.Fatal("a form rule without users: must never open")
	}
	// any_user opts out.
	g3 := newTest(t, formCfg(nil, true))
	interact(g3, shortcutPayload("U9"))
	if n := len(api.byMethod("views.open")); n != 1 {
		t.Fatalf("any_user: want the form opened, views.open x%d", n)
	}
}

func TestShortcutFilterMismatchDoesNotOpen(t *testing.T) {
	api := startFakeSlack(t)
	g := newTest(t, formCfg([]string{"U1"}, false))
	raw := strings.Replace(string(shortcutPayload("U1")), "conductor_handover", "other_shortcut", 1)
	interact(g, json.RawMessage(raw))
	if n := len(api.byMethod("views.open")); n != 0 {
		t.Fatal("a different callback_id must not open this trigger's form")
	}
}

func TestMentionWithFormPostsEphemeralButton(t *testing.T) {
	api := startFakeSlack(t)
	g := newTest(t, formCfg([]string{"U1"}, false))
	emit, got := collect()
	g.handleEvent(context.Background(), emit, json.RawMessage(
		`{"event":{"type":"app_mention","text":"<@B> help","user":"U1","channel":"C1","ts":"20.1","thread_ts":"19.0"}}`))
	if len(*got) != 0 {
		t.Fatal("a form mention must not fire before submission")
	}
	eph := api.byMethod("chat.postEphemeral")
	if len(eph) != 1 || eph[0].body["user"] != "U1" || eph[0].body["channel"] != "C1" || eph[0].body["thread_ts"] != "19.0" {
		t.Fatalf("ephemeral: %+v", eph)
	}
	if n := len(api.byMethod("chat.postMessage")); n != 0 {
		t.Fatal("nothing may be posted publicly")
	}
	blocks, _ := json.Marshal(eph[0].body["blocks"])
	var bl []struct {
		Elements []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		} `json:"elements"`
	}
	_ = json.Unmarshal(blocks, &bl)
	tok := ""
	for _, b := range bl {
		for _, e := range b.Elements {
			if e.ActionID == formOpenActionID {
				tok = e.Value
			}
		}
	}
	if tok == "" {
		t.Fatalf("no open-form button: %s", blocks)
	}

	click := func(user string) json.RawMessage {
		return json.RawMessage(`{"type":"block_actions","trigger_id":"trig-2","user":{"id":"` + user + `"},
			"actions":[{"action_id":"` + formOpenActionID + `","value":"` + tok + `"}]}`)
	}
	interact(g, click("U2"))
	if n := len(api.byMethod("views.open")); n != 0 {
		t.Fatal("only the mentioning user may open the form")
	}
	interact(g, click("U1"))
	opens := api.byMethod("views.open")
	if len(opens) != 1 || opens[0].body["trigger_id"] != "trig-2" {
		t.Fatalf("views.open: %+v", opens)
	}
	meta := opens[0].body["view"].(map[string]any)["private_metadata"].(string)
	emit2, got2 := collect()
	_, after := g.handleInteractive(context.Background(), emit2, submission(meta, goodValues))
	after()
	if len(*got2) != 1 {
		t.Fatalf("submission: want one trigger, got %d", len(*got2))
	}
	sc := (*got2)[0].Context["slack"].(map[string]any)
	if (*got2)[0].Kind != "app_mention" || sc["via"] != "mention" || sc["ts"] != "20.1" || sc["thread_ts"] != "19.0" {
		t.Fatalf("context: %+v", sc)
	}
}

func TestMentionWithFormIgnoresOtherUsers(t *testing.T) {
	api := startFakeSlack(t)
	g := newTest(t, formCfg([]string{"U1"}, false))
	emit, _ := collect()
	g.handleEvent(context.Background(), emit, json.RawMessage(
		`{"event":{"type":"app_mention","text":"<@B> help","user":"U2","channel":"C1","ts":"20.2"}}`))
	if n := len(api.byMethod("chat.postEphemeral")); n != 0 {
		t.Fatal("a user outside users: gets no form button")
	}
}

func TestValidateFormRules(t *testing.T) {
	cfg := formCfg(nil, false)
	if err := newTest(t, cfg).Validate(); err == nil || !strings.Contains(err.Error(), "needs users:") {
		t.Fatalf("want users: required, got %v", err)
	}
	if err := newTest(t, formCfg(nil, true)).Validate(); err != nil {
		t.Fatalf("any_user opt-out: %v", err)
	}
	cfg = formCfg([]string{"U1"}, false)
	cfg.Rules[0].Form.Fields[0].Default = "nope"
	if err := newTest(t, cfg).Validate(); err == nil {
		t.Fatal("a select default outside its options must be refused")
	}
	if err := newTest(t, Config{AppToken: "x", Rules: []Rule{{On: "reaction_added", Form: handoverForm(), Users: []string{"U1"},
		Actions: config.ActionSet{{Type: "agent"}}}}}).Validate(); err == nil {
		t.Fatal("form on reaction_added must be refused")
	}
}

// TestInteractiveAckCarriesErrors drives a view_submission through the real
// socket loop: the ACK frame must carry the response_action: errors payload.
func TestInteractiveAckCarriesErrors(t *testing.T) {
	startFakeSlack(t)
	meta := formMeta{Key: "0:slack.message_shortcut", Channel: "C1", TS: "10.1", User: "U1", Via: "shortcut", Callback: "conductor_handover"}.encode()
	b, _ := json.Marshal(meta)
	payload := `{"type":"view_submission","user":{"id":"U1"},"view":{"id":"V1","callback_id":"conductor_form","private_metadata":` +
		string(b) + `,"state":{"values":{}}}}`
	var ackFrame atomic.Value
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"interactive","envelope_id":"env-9","payload":`+payload+`}`))
		if _, data, err := c.Read(ctx); err == nil {
			ackFrame.Store(string(data))
		}
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"disconnect","reason":"done"}`))
	}))
	defer ws.Close()
	open := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"ok":true,"url":%q}`, ws.URL)
	}))
	defer open.Close()
	old := connectionsOpenURL
	connectionsOpenURL = open.URL
	defer func() { connectionsOpenURL = old }()

	g := newTest(t, formCfg([]string{"U1"}, false))
	emit, got := collect()
	_ = g.runOnce(context.Background(), emit)
	frame, _ := ackFrame.Load().(string)
	if !strings.Contains(frame, `"envelope_id":"env-9"`) || !strings.Contains(frame, `"response_action":"errors"`) {
		t.Fatalf("ack frame: %s", frame)
	}
	if len(*got) != 0 {
		t.Fatal("nothing fires on an invalid submission")
	}
}

func TestFormMetaFitsSlackLimit(t *testing.T) {
	m := formMeta{Key: "k", Text: strings.Repeat("é", 5000)}
	for i := 0; i < 50; i++ {
		m.Files = append(m.Files, slackFile{ID: "F", Name: strings.Repeat("n", 60), Mimetype: "image/png"})
	}
	if s := m.encode(); len(s) > maxPrivateMetadata {
		t.Fatalf("private_metadata %d bytes", len(s))
	}
}

// TestRuleSurvivesYAMLRoundTrip: the connectors lowering hands the
// integration its config through a YAML marshal/reparse; a form rule's form,
// users and filter must come out the other side intact and still match.
func TestRuleSurvivesYAMLRoundTrip(t *testing.T) {
	var spec struct {
		Filter *config.Filter `yaml:"filter"`
	}
	if err := yaml.Unmarshal([]byte("filter: { callback_id: cb, users: [U1] }"), &spec); err != nil {
		t.Fatal(err)
	}
	in := Config{AppToken: "x", Rules: []Rule{{On: "message_shortcut", Form: handoverForm(), Users: []string{"U1"},
		Key: "3:slack.message_shortcut", Filter: spec.Filter, Actions: config.ActionSet{{FlowRef: "3:slack.message_shortcut"}}}}}
	b, err := yaml.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Config
	if err := yaml.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	r := out.Rules[0]
	if r.Key != "3:slack.message_shortcut" || r.Form == nil || len(r.Form.Fields) != 3 || r.Form.Fields[1].Default != "plan" {
		t.Fatalf("rule after round trip: %+v", r)
	}
	if !r.preMatch("message_shortcut", map[string]any{"user": "U1", "callback_id": "cb"}) {
		t.Fatal("round-tripped filter should match")
	}
	if r.preMatch("message_shortcut", map[string]any{"user": "U1", "callback_id": "other"}) {
		t.Fatal("round-tripped filter must still check callback_id")
	}
}

func TestShortcutWithoutFormFiresImmediately(t *testing.T) {
	api := startFakeSlack(t)
	cfg := formCfg([]string{"U1"}, false)
	cfg.Rules[0].Form = nil
	g := newTest(t, cfg)
	_, kinds := interact(g, shortcutPayload("U1"))
	if len(kinds) != 1 || kinds[0] != "message_shortcut" {
		t.Fatalf("want an immediate fire, got %v", kinds)
	}
	if n := len(api.byMethod("views.open")); n != 0 {
		t.Fatal("no form, no modal")
	}
}
