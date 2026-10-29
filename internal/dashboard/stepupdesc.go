package dashboard

import (
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/dashboard/auth"
)

// stepupLabels remembers, for each step-up digest the server has handed out
// (an approval or purchase view, a config review), what confirming it does in
// words. approve-login shows that text before the operator signs, so they see
// the action a step-up confirms rather than only its purpose. The text comes
// from the server's own data for that digest, never from the requesting page.
type stepupLabels struct {
	mu  sync.Mutex
	m   map[string]stepupLabel // by purpose + "|" + digest
	now func() time.Time
}

type stepupLabel struct {
	text    string
	expires time.Time
}

const (
	stepupLabelTTL = 30 * time.Minute
	stepupLabelMax = 512
	stepupLabelLen = 300 // characters of text kept per label
)

func newStepupLabels(now func() time.Time) *stepupLabels {
	if now == nil {
		now = time.Now
	}
	return &stepupLabels{m: map[string]stepupLabel{}, now: now}
}

// note records what a step-up over (purpose, digest) confirms.
func (l *stepupLabels) note(purpose auth.Purpose, digest, text string) {
	if digest == "" || text == "" {
		return
	}
	if r := []rune(text); len(r) > stepupLabelLen {
		text = string(r[:stepupLabelLen-1]) + "…"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.m) >= stepupLabelMax {
		for k, v := range l.m {
			if now.After(v.expires) {
				delete(l.m, k)
			}
		}
		for k := range l.m {
			if len(l.m) < stepupLabelMax {
				break
			}
			delete(l.m, k)
		}
	}
	l.m[string(purpose)+"|"+digest] = stepupLabel{text: text, expires: now.Add(stepupLabelTTL)}
}

// describe is the auth Manager's DescribeStepup hook.
func (l *stepupLabels) describe(_ string, purpose auth.Purpose, digest string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, ok := l.m[string(purpose)+"|"+digest]
	if !ok || l.now().After(v.expires) {
		return ""
	}
	return v.text
}

// effectsText joins a review's effect descriptions for a label.
func effectsText(texts []string) string {
	return strings.Join(texts, "; ")
}
