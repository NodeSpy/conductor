package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/NodeSpy/conductor/internal/browser/cdp"
)

// Operator live view and takeover (design §4.5, NEW for the daemon): a CDP
// screencast of a running agent session relayed to the dashboard, and a
// step-up-gated takeover that pauses the agent and drives the same sandboxed
// page with CDP input — no separate headed browser, so it works over the web.
// Keystrokes and clicks are audited as a shape, never a value, like fills.

// ScreencastFrame is one frame of a session's live view: a base64 image (the raw
// CDP payload) and its format, ready to relay to the dashboard as-is.
type ScreencastFrame struct {
	DataB64 string `json:"data"`
	Format  string `json:"format"`
}

// InputEvent is one operator input to forward to a leased session's page.
type InputEvent struct {
	Type string `json:"type"` // mouse | key
	// mouse: kind is mousePressed|mouseReleased|mouseMoved; x/y in CSS px.
	Kind   string  `json:"kind,omitempty"`
	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
	Button string  `json:"button,omitempty"` // left|right|middle|none
	Clicks int     `json:"clicks,omitempty"`
	// key: kind is keyDown|keyUp|char; one printable rune in Text for char.
	Key  string `json:"key,omitempty"`
	Text string `json:"text,omitempty"`
}

func (m *Manager) sessionByID(id string) *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		if s.id == id {
			return s
		}
	}
	return nil
}

// activeTab returns the session's active instance and its attached current tab,
// or an error when nothing is running.
func (m *Manager) activeTab(ctx context.Context, s *session) (*Instance, *tab, error) {
	s.mu.Lock()
	in := s.active
	s.mu.Unlock()
	if in == nil || isDone(in) {
		return nil, nil, fmt.Errorf("browser: session %s has no running browser", s.id)
	}
	t, err := in.current(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := in.attach(ctx, t); err != nil {
		return nil, nil, err
	}
	return in, t, nil
}

// Screencast streams a session's active tab to fn until ctx ends (or the tab
// goes away). It is the live view behind the dashboard's WebSocket relay. The
// frames are images only; nothing a credential filled is ever in them because
// the agent's own fills never render a value the operator could read back any
// differently than the agent sees it — the operator is watching the same page.
func (m *Manager) Screencast(ctx context.Context, id string, fn func(ScreencastFrame) error) error {
	s := m.sessionByID(id)
	if s == nil {
		return fmt.Errorf("browser: no session %q (conductor browser sessions)", id)
	}
	in, t, err := m.activeTab(ctx, s)
	if err != nil {
		return err
	}
	conn := in.conn()
	sess := t.session
	// Each frame arrives as a Page.screencastFrame event on the tab's session;
	// we decode nothing, relay the base64 payload, and must ack by sessionId or
	// the browser stops sending.
	frames := make(chan cdp.Event, 8)
	cancel := conn.Listen(func(ev cdp.Event) {
		if ev.Session != sess || ev.Method != "Page.screencastFrame" {
			return
		}
		select {
		case frames <- ev:
		default: // drop a frame rather than block the CDP reader
		}
	})
	defer cancel()
	if err := conn.Call(ctx, sess, "Page.startScreencast", map[string]any{
		"format": "jpeg", "quality": 60, "maxWidth": 1280, "maxHeight": 800, "everyNthFrame": 1,
	}, nil); err != nil {
		return err
	}
	defer func() {
		stop, c := context.WithTimeout(context.Background(), 2*time.Second)
		_ = conn.Call(stop, sess, "Page.stopScreencast", map[string]any{}, nil)
		c()
	}()
	m.audit(map[string]any{"action": "screencast", "identity": s.caller.Identity, "outcome": "ok", "by": "operator"})
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-frames:
			var p struct {
				Data      string `json:"data"`
				SessionID int    `json:"sessionId"`
			}
			if err := json.Unmarshal(ev.Params, &p); err != nil {
				continue
			}
			_ = conn.Call(ctx, sess, "Page.screencastFrameAck", map[string]any{"sessionId": p.SessionID}, nil)
			if err := fn(ScreencastFrame{DataB64: p.Data, Format: "jpeg"}); err != nil {
				return err
			}
		}
	}
}

// TakeLease gives an operator exclusive control of a session: the agent's next
// action waits (doWith), and Input drives the page until ReleaseLease. It is
// gated by a passkey step-up at the dashboard (design §4.5).
func (m *Manager) TakeLease(id, operator string) error {
	s := m.sessionByID(id)
	if s == nil {
		return fmt.Errorf("browser: no session %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leaseBy != "" && s.leaseBy != operator {
		return fmt.Errorf("browser: %s already has control", s.leaseBy)
	}
	if s.active == nil || isDone(s.active) {
		return fmt.Errorf("browser: session %s has no running browser", id)
	}
	s.leaseBy, s.leaseCh = operator, make(chan struct{})
	m.audit(map[string]any{"action": "takeover", "identity": s.caller.Identity, "operator": operator, "outcome": "ok", "by": "operator"})
	return nil
}

// ReleaseLease returns control to the agent (its next action resumes with a
// fresh snapshot). Idempotent.
func (m *Manager) ReleaseLease(id, operator string) error {
	s := m.sessionByID(id)
	if s == nil {
		return fmt.Errorf("browser: no session %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leaseBy == "" {
		return nil
	}
	if s.leaseCh != nil {
		close(s.leaseCh)
	}
	s.leaseBy, s.leaseCh = "", nil
	m.audit(map[string]any{"action": "takeover-release", "identity": s.caller.Identity, "operator": operator, "outcome": "ok", "by": "operator"})
	return nil
}

// LeaseBy reports who holds the takeover lease ("" when free).
func (m *Manager) LeaseBy(id string) string {
	s := m.sessionByID(id)
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaseBy
}

// Input forwards one operator input to a leased session's page. It refuses
// unless the operator holds the lease. Audited as a shape, never a value.
func (m *Manager) Input(ctx context.Context, id, operator string, ev InputEvent) error {
	s := m.sessionByID(id)
	if s == nil {
		return fmt.Errorf("browser: no session %q", id)
	}
	if by := m.LeaseBy(id); by != operator {
		return fmt.Errorf("browser: take over first (held by %q)", by)
	}
	in, t, err := m.activeTab(ctx, s)
	if err != nil {
		return err
	}
	conn := in.conn()
	switch ev.Type {
	case "mouse":
		clicks := ev.Clicks
		if clicks == 0 && (ev.Kind == "mousePressed" || ev.Kind == "mouseReleased") {
			clicks = 1
		}
		p := map[string]any{"type": ev.Kind, "x": ev.X, "y": ev.Y, "button": orDefault(ev.Button, "none"), "clickCount": clicks}
		if err := conn.Call(ctx, t.session, "Input.dispatchMouseEvent", p, nil); err != nil {
			return err
		}
		m.audit(map[string]any{"action": "input", "detail": "mouse " + ev.Kind, "identity": s.caller.Identity, "operator": operator, "outcome": "ok", "by": "operator"})
	case "key":
		p := map[string]any{"type": ev.Kind}
		if ev.Text != "" {
			p["text"] = ev.Text
		}
		if ev.Key != "" {
			p["key"] = ev.Key
		}
		if err := conn.Call(ctx, t.session, "Input.dispatchKeyEvent", p, nil); err != nil {
			return err
		}
		// A keystroke is audited as a length only, like a fill (design §4.5).
		m.audit(map[string]any{"action": "input", "detail": fmt.Sprintf("key · %d chars", len(ev.Text)), "identity": s.caller.Identity, "operator": operator, "outcome": "ok", "by": "operator"})
	default:
		return fmt.Errorf("browser: unknown input %q", ev.Type)
	}
	return nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

var _ = base64.StdEncoding
var _ = time.Now
