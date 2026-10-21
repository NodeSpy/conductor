//go:build linux || darwin

package browser

import (
	"context"
	"strings"
	"testing"
)

// TestRedteamEvaluateNoSideEffect: the only verb that runs the agent's own
// JavaScript in the page is evaluate, and it must be read-only — the whole
// anti-injection model depends on the agent being unable to install an input
// listener, a value-setter override, a MutationObserver, or any other hook
// that would capture a value conductor fills afterward. Every side-effecting
// expression must be refused, before any credential is filled.
func TestRedteamEvaluateNoSideEffect(t *testing.T) {
	r := newCredRig(t)
	r.st.Evaluate = []string{"agent:test@*"}
	c := r.caller("op", RequesterOperator)
	r.do(c, "open", map[string]any{"url": r.bound + "/login"})

	// A read is allowed (sanity).
	if _, err := r.m.Do(context.Background(), c, "evaluate", map[string]any{"expr": "document.querySelector('input') ? 'has-input' : 'none'"}); err != nil {
		t.Fatalf("a read was refused: %v", err)
	}

	// Each of these would install a capture hook or mutate state; all refused.
	hooks := []string{
		`document.addEventListener('input', e => { window.__x = e.target.value; }, true)`,
		`document.querySelector('input').addEventListener('change', e => {})`,
		`Object.defineProperty(HTMLInputElement.prototype, 'value', {set(v){ window.__x = v; }})`,
		`HTMLInputElement.prototype.__defineSetter__('value', function(v){})`,
		`new MutationObserver(() => {}).observe(document.body, {attributes: true, subtree: true})`,
		`Object.prototype.__stolen = 1`,
		`window.fetch = () => {}`,
		`window.__x = 1`,
		`document.title = 'leak'`,
		`document.querySelector('input').value = 'x'`,
		`document.body.appendChild(document.createElement('div'))`,
		`document.querySelector('input').setAttribute('data-x', '1')`,
		`localStorage.setItem('a', 'b')`,
		`document.cookie = 'a=b'`,
		`navigator.clipboard.readText()`,
		`document.execCommand('copy')`,
	}
	for _, h := range hooks {
		_, err := r.m.Do(context.Background(), c, "evaluate", map[string]any{"expr": h})
		if err == nil {
			t.Errorf("LEAK: evaluate ran a side effect: %q", h)
			continue
		}
		if !strings.Contains(err.Error(), "side effect") {
			// Not a side-effect refusal — note it (a different refusal is still
			// safe, but we want to know).
			t.Logf("refused (not as a side effect): %q -> %v", h, err)
		}
	}
}
