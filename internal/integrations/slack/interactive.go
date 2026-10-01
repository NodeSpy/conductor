package slack

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/NodeSpy/conductor/internal/core"
)

// Interactive (Socket Mode `interactive` envelope) handling: message
// shortcuts, the ephemeral "open form" button a mention-with-form posts, and
// modal submissions. Every one of these must be ACKed within 3 seconds, and a
// trigger_id is only good for views.open for 3 seconds — so the decisions
// here (does a rule match, which form opens) are made synchronously in the
// socket loop, and the follow-up work (views.open, emit) runs right after
// the ACK is written.

// Callback/action ids conductor owns on the app's surfaces.
const (
	formViewCallbackID = "conductor_form"
	formOpenActionID   = "conductor_form_open"
)

// pendingTTL bounds how long a mention's "open form" button stays usable.
const pendingTTL = 30 * time.Minute

// maxPendingForms bounds the button-token table.
const maxPendingForms = 1024

// interactivePayload is the subset of Slack's interaction payloads read here
// (message_action, block_actions, view_submission).
type interactivePayload struct {
	Type       string `json:"type"`
	CallbackID string `json:"callback_id"`
	TriggerID  string `json:"trigger_id"`
	User       struct {
		ID string `json:"id"`
	} `json:"user"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
	Message struct {
		TS       string      `json:"ts"`
		ThreadTS string      `json:"thread_ts"`
		Text     string      `json:"text"`
		Files    []slackFile `json:"files"`
	} `json:"message"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
	View struct {
		ID              string `json:"id"`
		CallbackID      string `json:"callback_id"`
		PrivateMetadata string `json:"private_metadata"`
		State           struct {
			Values map[string]map[string]stateValue `json:"values"`
		} `json:"state"`
	} `json:"view"`
}

// stateValue is one input element's submitted state.
type stateValue struct {
	Type           string `json:"type"`
	Value          string `json:"value"`
	SelectedOption *struct {
		Value string `json:"value"`
	} `json:"selected_option"`
}

// slackFile is the file metadata Slack attaches to a message.
type slackFile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Mimetype string `json:"mimetype"`
}

func filesCtx(fs []slackFile) []any {
	out := make([]any, 0, len(fs))
	for _, f := range fs {
		out = append(out, map[string]any{"id": f.ID, "name": f.Name, "mimetype": f.Mimetype})
	}
	return out
}

// formMeta is a modal's private_metadata: which rule the form belongs to
// and the message/thread it was opened on. Slack stores it server-side with
// the view and returns it unchanged on submission.
type formMeta struct {
	Key      string      `json:"k"`
	Channel  string      `json:"c"`
	TS       string      `json:"ts"`
	ThreadTS string      `json:"tt,omitempty"`
	User     string      `json:"u"`
	Via      string      `json:"v"`
	Callback string      `json:"cb,omitempty"`
	Text     string      `json:"x,omitempty"`
	Files    []slackFile `json:"f,omitempty"`
}

// maxPrivateMetadata is Slack's private_metadata limit.
const maxPrivateMetadata = 3000

func (m formMeta) encode() string {
	b, _ := json.Marshal(m)
	if len(b) <= maxPrivateMetadata {
		return string(b)
	}
	m.Files = nil
	b, _ = json.Marshal(m)
	for len(b) > maxPrivateMetadata && m.Text != "" {
		m.Text = clipRunes(m.Text, utf8.RuneCountInString(m.Text)/2)
		b, _ = json.Marshal(m)
	}
	return string(b)
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// formState holds the mention-path "open form" buttons awaiting a click.
type formState struct {
	mu      sync.Mutex
	pending map[string]pendingForm
	order   []string
}

type pendingForm struct {
	meta    formMeta
	expires time.Time
}

func (s *formState) add(m formMeta) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	tok := hex.EncodeToString(b[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = map[string]pendingForm{}
	}
	s.pending[tok] = pendingForm{meta: m, expires: time.Now().Add(pendingTTL)}
	s.order = append(s.order, tok)
	for len(s.order) > maxPendingForms {
		delete(s.pending, s.order[0])
		s.order = s.order[1:]
	}
	return tok
}

func (s *formState) get(tok string) (formMeta, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[tok]
	if !ok || time.Now().After(p.expires) {
		return formMeta{}, false
	}
	return p.meta, true
}

// ruleByKey finds a form rule by its stable key.
func (g *Integration) ruleByKey(key string) (Rule, bool) {
	for _, r := range g.cfg.Rules {
		if r.Form != nil && r.Key == key {
			return r, true
		}
	}
	return Rule{}, false
}

// ruleKey is a rule's identity for form round-trips: its configured Key, or
// its position (legacy rules carry no key).
func (r Rule) ruleKey(i int) string {
	if r.Key != "" {
		return r.Key
	}
	return fmt.Sprintf("rule-%d", i)
}

// preMatch decides, at shortcut/mention time, whether rule r applies to the
// acting user and context. It checks the rule's users (a form or shortcut
// rule with no users and no any_user never matches — the same default the
// config validator enforces, repeated here so a rule that somehow reaches
// the integration without it still refuses) and then the trigger's own
// filter, evaluated with the same matcher the flow runner uses.
func (r Rule) preMatch(on string, sctx map[string]any) bool {
	user, _ := sctx["user"].(string)
	if len(r.Users) > 0 {
		if !containsFold(r.Users, user) {
			return false
		}
	} else if !r.AnyUser && (r.Form != nil || on == "message_shortcut") {
		return false
	}
	if r.Filter == nil {
		return true
	}
	ok, err := r.Filter.Eval(map[string]any{"slack": sctx}, func(key string, val any, facts map[string]any) (bool, error) {
		return FilterMatch(on, map[string]any{key: val}, facts)
	})
	return err == nil && ok
}

// handleInteractive processes one `interactive` envelope payload. It returns
// the ACK payload (nil for a bare ACK) and work to run once the ACK is sent.
func (g *Integration) handleInteractive(ctx context.Context, emit core.EmitFunc, raw json.RawMessage) (ack any, after func()) {
	var p interactivePayload
	if json.Unmarshal(raw, &p) != nil {
		return nil, nil
	}
	switch p.Type {
	case "message_action":
		return nil, g.onMessageShortcut(ctx, emit, p)
	case "block_actions":
		return nil, g.onBlockActions(ctx, p)
	case "view_submission":
		if p.View.CallbackID != formViewCallbackID {
			return nil, nil
		}
		return g.onViewSubmission(ctx, emit, p)
	}
	return nil, nil
}

// onMessageShortcut: a message shortcut was used on a message. A matching
// rule with a form opens it (the first one — one trigger_id opens one
// modal); a matching rule without one fires immediately.
func (g *Integration) onMessageShortcut(ctx context.Context, emit core.EmitFunc, p interactivePayload) func() {
	ev := evt{
		text: p.Message.Text, user: p.User.ID, channel: p.Channel.ID,
		ts: p.Message.TS, threadTS: firstNonEmpty(p.Message.ThreadTS, p.Message.TS),
		via: "shortcut", callbackID: p.CallbackID, files: p.Message.Files,
	}
	sctx := ev.context()
	var direct []Rule
	var form *Rule
	var formIdx int
	for i, r := range g.cfg.Rules {
		if r.On != "message_shortcut" || !r.preMatch("message_shortcut", sctx) {
			continue
		}
		if r.Form == nil {
			direct = append(direct, r)
		} else if form == nil {
			r := r
			form, formIdx = &r, i
		}
	}
	if form == nil && len(direct) == 0 {
		log.Printf("slack[%s]: message shortcut %q by %s matched no trigger", g.name, p.CallbackID, p.User.ID)
		return nil
	}
	return func() {
		if form != nil {
			g.openForm(ctx, p.TriggerID, form.Form, formMeta{
				Key: form.ruleKey(formIdx), Channel: ev.channel, TS: ev.ts, ThreadTS: ev.threadTS,
				User: ev.user, Via: ev.via, Callback: ev.callbackID, Text: clipRunes(ev.text, 1500), Files: ev.files,
			})
		}
		if len(direct) > 0 {
			g.fireRules(ctx, emit, "message_shortcut", "message_shortcut:"+ev.channel+":"+ev.ts+":"+p.TriggerID, ev, direct)
		}
	}
}

// offerForms is the mention-with-form path: for the first form rule that
// matches the mention, post an ephemeral message (visible only to the
// mentioning user, in the thread) with a button that opens the form.
func (g *Integration) offerForms(ctx context.Context, ev evt) {
	sctx := ev.context()
	for i, r := range g.cfg.Rules {
		if r.On != "app_mention" || r.Form == nil || !r.preMatch("app_mention", sctx) {
			continue
		}
		if !g.seen.Add("app_mention-form:" + ev.channel + ":" + ev.ts) {
			return
		}
		tok := g.forms.add(formMeta{
			Key: r.ruleKey(i), Channel: ev.channel, TS: ev.ts, ThreadTS: ev.threadTS,
			User: ev.user, Via: "mention", Text: clipRunes(ev.text, 1500), Files: ev.files,
		})
		text := "Open the form to continue."
		blocks := []any{
			map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}},
			map[string]any{"type": "actions", "elements": []any{map[string]any{
				"type": "button", "action_id": formOpenActionID, "value": tok,
				"text": plain(r.Form.title()), "style": "primary",
			}}},
		}
		payload := map[string]any{"channel": ev.channel, "user": ev.user, "text": text, "blocks": blocks}
		if ev.threadTS != "" {
			payload["thread_ts"] = ev.threadTS
		}
		if err := g.callJSON(ctx, chatPostEphemeralURL, payload); err != nil {
			log.Printf("slack[%s]: post form button: %v", g.name, err)
		}
		return
	}
}

// onBlockActions: the ephemeral "open form" button was clicked. Only the
// user it was offered to may open it.
func (g *Integration) onBlockActions(ctx context.Context, p interactivePayload) func() {
	for _, a := range p.Actions {
		if a.ActionID != formOpenActionID {
			continue
		}
		meta, ok := g.forms.get(a.Value)
		if !ok {
			log.Printf("slack[%s]: form button clicked after it expired", g.name)
			return nil
		}
		if meta.User != p.User.ID {
			log.Printf("slack[%s]: form button offered to %s clicked by %s — ignored", g.name, meta.User, p.User.ID)
			return nil
		}
		r, ok := g.ruleByKey(meta.Key)
		if !ok {
			return nil
		}
		return func() { g.openForm(ctx, p.TriggerID, r.Form, meta) }
	}
	return nil
}

// onViewSubmission validates a submitted form and, if it holds, fires the
// form's rule. Validation errors are returned in the ACK (Slack shows them
// inline and keeps the modal open); nothing fires.
func (g *Integration) onViewSubmission(ctx context.Context, emit core.EmitFunc, p interactivePayload) (any, func()) {
	var meta formMeta
	if json.Unmarshal([]byte(p.View.PrivateMetadata), &meta) != nil {
		return nil, nil
	}
	r, ok := g.ruleByKey(meta.Key)
	if !ok {
		// A legacy rule's key is positional; resolve it the same way.
		for i, cand := range g.cfg.Rules {
			if cand.Form != nil && cand.ruleKey(i) == meta.Key {
				r, ok = cand, true
				break
			}
		}
	}
	if !ok {
		log.Printf("slack[%s]: form submission for unknown trigger %q (config changed?)", g.name, meta.Key)
		return nil, nil
	}
	on := "app_mention"
	if meta.Via == "shortcut" {
		on = "message_shortcut"
	}
	if r.On != on || meta.User != p.User.ID {
		log.Printf("slack[%s]: form submission does not match its trigger (on=%s via=%s user=%s/%s) — ignored", g.name, r.On, meta.Via, meta.User, p.User.ID)
		return nil, nil
	}
	ev := evt{
		text: meta.Text, user: p.User.ID, channel: meta.Channel, ts: meta.TS, threadTS: meta.ThreadTS,
		via: meta.Via, callbackID: meta.Callback, files: meta.Files,
	}
	// Re-check the rule against the submitter before anything else: the
	// config may have changed while the modal was open.
	if !r.preMatch(on, ev.context()) {
		log.Printf("slack[%s]: form submission by %s no longer matches its trigger — ignored", g.name, p.User.ID)
		return nil, nil
	}
	vals, errs := r.Form.formValues(p.View.State.Values)
	if len(errs) > 0 {
		return map[string]any{"response_action": "errors", "errors": errs}, nil
	}
	ev.form = vals
	return nil, func() {
		g.fireRules(ctx, emit, on, on+":"+ev.channel+":"+ev.ts+":"+p.View.ID, ev, []Rule{r})
	}
}

// openForm opens a rule's form as a modal on trigger_id.
func (g *Integration) openForm(ctx context.Context, triggerID string, f *Form, meta formMeta) {
	if triggerID == "" || f == nil {
		return
	}
	view := map[string]any{
		"type": "modal", "callback_id": formViewCallbackID,
		"title": plain(f.title()), "submit": plain(f.submit()), "close": plain("Cancel"),
		"private_metadata": meta.encode(),
		"blocks":           f.blocks(),
	}
	if err := g.callJSON(ctx, viewsOpenURL, map[string]any{"trigger_id": triggerID, "view": view}); err != nil {
		log.Printf("slack[%s]: views.open: %v", g.name, err)
	}
}

// callJSON POSTs a JSON payload to a Slack Web API method as the bot and
// returns any transport or Slack-level error.
func (g *Integration) callJSON(ctx context.Context, url string, payload any) error {
	if g.cfg.BotToken == "" {
		return fmt.Errorf("no bot_token")
	}
	body, _ := json.Marshal(payload)
	req, err := newJSONRequest(ctx, url, body, g.cfg.BotToken)
	if err != nil {
		return err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("HTTP %d: %w", resp.StatusCode, err)
	}
	if !out.OK {
		return fmt.Errorf("%s", strings.TrimSpace(out.Error))
	}
	return nil
}
