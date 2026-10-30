package dashboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
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

// A config confirmation is built from what changes, leaf by leaf, between the
// live config and the candidate (configedit's classify), never from how the
// edit was staged: an ssh_key swapped by staging the whole operators map shows
// as the ssh_key leaf it changes. Every leaf outside the inert paths, and every
// widening leaf, is shown in full with its old and new value. Only the other
// (inert) leaves can be shortened, or dropped past descEditMax.
const (
	descEditMax  = 20  // inert leaves listed
	descEditRune = 200 // characters per inert leaf
)

// describeConfig is a config apply's or restore's description: what, then
// every guarded section and every widening change, then every changed leaf
// under a guarded or widening path in full, then the other changed leaves
// (shortened, and said so), then the diff size. Every value in it is made
// safe to print.
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
	var full, other []configedit.LeafChange
	for _, c := range rev.Changes {
		if c.Guarded || c.Widens {
			full = append(full, c)
		} else {
			other = append(other, c)
		}
	}
	if len(full) > 0 {
		b.WriteString("\nchanges to guarded settings (in full):")
		for _, c := range full {
			b.WriteString("\n  - " + auth.SafeText(leafText(c)))
		}
	}
	if len(other) > 0 {
		b.WriteString("\nother changes:")
		for i, c := range other {
			if i == descEditMax {
				fmt.Fprintf(&b, "\n  … and %d more (not shown here; the page lists them all)", len(other)-descEditMax)
				break
			}
			t := []rune(leafText(c))
			text := string(t)
			if len(t) > descEditRune {
				text = fmt.Sprintf("%s… (shortened: %d more characters)", string(t[:descEditRune]), len(t)-descEditRune)
			}
			b.WriteString("\n  - " + auth.SafeText(text))
		}
	}
	fmt.Fprintf(&b, "\ndiff: %d lines added, %d removed", rev.Diff.Added, rev.Diff.Removed)
	return b.String()
}

// leafText is one changed leaf: its path, and its value before and after.
// Strings are quoted, so spaces and empty values show.
func leafText(c configedit.LeafChange) string {
	switch {
	case !c.Had:
		return fmt.Sprintf("%s: (none) → %s", c.Path, leafValue(c.After))
	case !c.Has:
		return fmt.Sprintf("%s: %s → (removed)", c.Path, leafValue(c.Before))
	}
	return fmt.Sprintf("%s: %s → %s", c.Path, leafValue(c.Before), leafValue(c.After))
}

func leafValue(v any) string {
	switch x := v.(type) {
	case string:
		return strconv.Quote(x)
	case map[string]any:
		return "{}"
	case []any:
		return "[]"
	case nil:
		return "null"
	}
	return fmt.Sprintf("%v", v)
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
