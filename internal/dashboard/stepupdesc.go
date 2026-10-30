package dashboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/configedit"
	"github.com/NodeSpy/conductor/internal/dashboard/auth"
)

// stepupLabels remembers, per session, what confirming each step-up digest the
// server handed that session (an approval or purchase view, a config review, a
// wallet change or take-over refused for want of one) does, in words. An SSH
// step-up binds the description when it begins and approve-login shows it, so
// the operator reads the action from the server's own data, never from the
// requesting page. Entries are per session: one session's reviews can't evict
// another's, and a session that evicts its own only stops its own SSH step-ups.
type stepupLabels struct {
	mu  sync.Mutex
	m   map[string]map[string]stepupLabel // session → purpose|digest → label
	now func() time.Time
}

type stepupLabel struct {
	text    string
	expires time.Time
}

const (
	stepupLabelTTL        = 30 * time.Minute
	stepupLabelPerSession = 64
)

func newStepupLabels(now func() time.Time) *stepupLabels {
	if now == nil {
		now = time.Now
	}
	return &stepupLabels{m: map[string]map[string]stepupLabel{}, now: now}
}

// note records what a step-up over (purpose, digest) by r's session confirms.
// Without a session there is no step-up to describe.
func (l *stepupLabels) note(r *http.Request, purpose auth.Purpose, digest, text string) {
	sess, ok := auth.SessionFrom(r)
	if !ok || digest == "" || text == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for id, per := range l.m {
		for k, v := range per {
			if now.After(v.expires) {
				delete(per, k)
			}
		}
		if len(per) == 0 {
			delete(l.m, id)
		}
	}
	per := l.m[sess.ID]
	if per == nil {
		per = map[string]stepupLabel{}
		l.m[sess.ID] = per
	}
	key := string(purpose) + "|" + digest
	if _, have := per[key]; !have && len(per) >= stepupLabelPerSession {
		oldest := ""
		for k, v := range per {
			if oldest == "" || v.expires.Before(per[oldest].expires) {
				oldest = k
			}
		}
		delete(per, oldest)
	}
	per[key] = stepupLabel{text: text, expires: now.Add(stepupLabelTTL)}
}

// describe is the auth Manager's DescribeStepup hook.
func (l *stepupLabels) describe(session string, purpose auth.Purpose, digest string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, ok := l.m[session][string(purpose)+"|"+digest]
	if !ok || l.now().After(v.expires) {
		return ""
	}
	return v.text
}

// Shortening applies only to staged edits outside every guarded and widening
// path (cosmetic ones); an edit under one is shown in full, so the operator
// reads the exact new ssh_key or command, not its first characters.
const (
	descEditMax  = 20  // staged edits listed
	descEditRune = 200 // characters per staged edit
)

// describeConfig is a config apply's or restore's description: what, then
// every guarded section and every widening change in full, then the staged
// edits under them in full, then the other staged edits (shortened, and said
// so), then the diff size. Every value in it is made safe to print.
func describeConfig(what string, rev configedit.Review) string {
	var b strings.Builder
	b.WriteString(auth.SafeText(what))
	list := func(title string, es []configedit.Effect) {
		if len(es) == 0 {
			return
		}
		b.WriteString("\n" + title)
		for _, e := range es {
			b.WriteString("\n  - " + auth.SafeText(e.Text))
		}
	}
	list("guarded (why it needs this confirmation):", rev.Guarded)
	list("loosens a guardrail:", rev.Widening)
	var guarded, cosmetic []configedit.Effect
	for _, e := range rev.Effects {
		if underAny(e.Path, rev.Guarded) || underAny(e.Path, rev.Widening) {
			guarded = append(guarded, e)
		} else {
			cosmetic = append(cosmetic, e)
		}
	}
	list("staged edits to guarded settings (in full):", guarded)
	if len(cosmetic) > 0 {
		b.WriteString("\nother staged edits:")
		for i, e := range cosmetic {
			if i == descEditMax {
				fmt.Fprintf(&b, "\n  … and %d more (not shown here; the page lists them all)", len(cosmetic)-descEditMax)
				break
			}
			t := []rune(e.Text)
			if len(t) > descEditRune {
				e.Text = fmt.Sprintf("%s… (shortened: %d more characters)", string(t[:descEditRune]), len(t)-descEditRune)
			}
			b.WriteString("\n  - " + auth.SafeText(e.Text))
		}
	}
	fmt.Fprintf(&b, "\ndiff: %d lines added, %d removed", rev.Diff.Added, rev.Diff.Removed)
	return b.String()
}

// underAny reports whether the dotted path is one of es's paths or under one;
// "(config)" (the config as a whole) covers every path.
func underAny(path string, es []configedit.Effect) bool {
	for _, e := range es {
		if e.Path == "(config)" || path == e.Path || strings.HasPrefix(path, e.Path+".") {
			return true
		}
	}
	return false
}

// describeAction is an approval's or purchase's description: the decision,
// the canonical action pretty-printed in full, then the requester's own words
// labelled as theirs. view is the ApprovalView/PurchaseView JSON: {"action":
// …, "requester_summary": "…", and display fields}.
func describeAction(heading, view string) string {
	var v struct {
		Action    json.RawMessage `json:"action"`
		Requester string          `json:"requester_summary"`
	}
	var b strings.Builder
	b.WriteString(auth.SafeText(heading))
	if err := json.Unmarshal([]byte(view), &v); err != nil || len(v.Action) == 0 {
		b.WriteString("\nthe action: (the server has no record of it)")
		return b.String()
	}
	b.WriteString("\nthe action, exactly as it will run:")
	for _, l := range auth.SafeLines(prettyJSON(v.Action)) {
		b.WriteString("\n  " + l)
	}
	if v.Requester != "" {
		b.WriteString("\nthe requester's own words (not checked): " + auth.SafeText(v.Requester))
	}
	return b.String()
}

// prettyJSON indents raw JSON without HTML escaping; raw is returned as is if
// it doesn't parse.
func prettyJSON(raw []byte) string {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return string(raw)
	}
	return out.String()
}
