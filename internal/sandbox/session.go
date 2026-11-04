package sandbox

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Egress sessions: every credential the proxy mints is one launch's
// session — attributed to its run, counted, bounded, and closed with it.
// Revoking a session (its launch ended) closes its open tunnels, not only
// its future CONNECTs; a session over a byte cap has its tunnels closed and
// further requests refused.

// EgressLimits bound one session. Zero fields take the defaults; a
// negative field is off.
type EgressLimits struct {
	Idle     time.Duration // no bytes either way for this long closes a tunnel (default 10m)
	Upload   int64         // bytes sent, all tunnels (default 512 MiB)
	Download int64         // bytes received, all tunnels (default 20 GiB)
}

// NoEgressLimits turns every limit off.
var NoEgressLimits = EgressLimits{Idle: -1, Upload: -1, Download: -1}

// SessionOpts describe one session.
type SessionOpts struct {
	// Run attributes the session's traffic (a dispatch id, a session).
	Run    string
	Limits EgressLimits
	// RequireTLS: every CONNECT tunnel must open with a TLS ClientHello
	// naming the CONNECT host (SNI; no encrypted ClientHello), read before
	// the upstream is dialed.
	RequireTLS bool
	// NonTLS are host:port patterns (as egress) exempt from RequireTLS: the
	// launch's allowance for raw traffic (an SSH bastion), audited with
	// NonTLSBy as the tunnel's AllowedBy.
	NonTLS   []string
	NonTLSBy string
	// Shape is what a tunnel of a suspicious shape gets (see ShapeEvent):
	// ShapeAlert ("" too), ShapeClose (the tunnel is closed) or ShapeOff.
	// The features are on every ConnRecord either way.
	Shape string
	// ShapeAllowed are host:port patterns (as egress) whose tunnels'
	// shapes are reported as allowed (by ShapeAllowedBy): a scoped
	// allowance's tunnel hosts. ShapeAllowAll: every tunnel's (an
	// allowance for a tunnel tool to any host).
	ShapeAllowed   []string
	ShapeAllowAll  bool
	ShapeAllowedBy string
	// OnShape receives each flagged tunnel, once, while it is open (not
	// called with Shape off).
	OnShape func(ShapeEvent)
}

// Tunnel shapes and what a flagged one gets.
const (
	ShapeAlert = "alert"
	ShapeClose = "close"
	ShapeOff   = "off"

	// ShapeInteractive: a long-lived, low-volume tunnel in which the far
	// end repeatedly speaks first after a silence — a remote operator's
	// commands, not a client's requests.
	ShapeInteractive = "interactive"
	// ShapeBulkUpload: a tunnel that sends much and receives little.
	ShapeBulkUpload = "bulk_upload"
)

// Shape thresholds (vars for tests). Timing cannot tell a remote
// operator's commands from a server that answers every request slowly
// (long polling, a websocket): both are the client speaking, a silence,
// the server speaking. The thresholds keep that to answers of 5s or more,
// five times over, on a small tunnel open two minutes or more — rare in
// build traffic (model traffic goes through the gateway, not here) — and
// a flagged tunnel is alerted on by default, not closed.
var (
	// ServerFirstGap is the silence after which the server's bytes count
	// as it speaking first (the client spoke last).
	ServerFirstGap            = 5 * time.Second
	interactiveMinAge         = 2 * time.Minute
	interactiveMinTurns int32 = 5
	interactiveMaxBytes int64 = 4 << 20
	bulkUploadMin       int64 = 16 << 20
	bulkUploadRatio     int64 = 8
)

// ShapeEvent is a tunnel flagged by its shape.
type ShapeEvent struct {
	Run, Target, Kind, Reason string
	// Action is ShapeAlert, ShapeClose or "allowed" (an allowance covers
	// the tunnel); AllowedBy names that allowance.
	Action, AllowedBy string
	Up, Down          int64
	Age, MaxIdle      time.Duration
	ServerFirst       int
}

// Default egress limits.
const (
	DefaultEgressIdle     = 10 * time.Minute
	DefaultEgressUpload   = 512 << 20
	DefaultEgressDownload = 20 << 30
)

func (l EgressLimits) resolved() EgressLimits {
	if l.Idle == 0 {
		l.Idle = DefaultEgressIdle
	}
	if l.Upload == 0 {
		l.Upload = DefaultEgressUpload
	}
	if l.Download == 0 {
		l.Download = DefaultEgressDownload
	}
	return l
}

// ConnRecord is one CONNECT or proxied request, allowed or not: the audit
// row. The shape fields (MaxIdle, ServerFirst) are logged so later
// detection thresholds can come from data; nothing enforces on them.
type ConnRecord struct {
	Run    string `json:"run,omitempty"`
	Target string `json:"target"`
	SNI    string `json:"sni,omitempty"`
	// Proto is the tunnel's protocol as the check recognized it (ProtoTLS,
	// ProtoHTTP, ProtoHTTP2; ProtoSSH on a refusal; "" unchecked).
	Proto    string        `json:"proto,omitempty"`
	Outcome  string        `json:"outcome"` // allowed | denied | refused | closed
	Reason   string        `json:"reason,omitempty"`
	Up       int64         `json:"up"`
	Down     int64         `json:"down"`
	Duration time.Duration `json:"duration"`
	// MaxIdle is the longest gap with no bytes either way; ServerFirst
	// counts the times the server spoke first after a gap of 30s or more
	// (an interactive channel's shape).
	MaxIdle     time.Duration `json:"max_idle,omitempty"`
	ServerFirst int           `json:"server_first,omitempty"`
	// AllowedBy names the allowance that let the tunnel skip a check (its
	// scope); "" for an ordinary tunnel.
	AllowedBy string `json:"allowed_by,omitempty"`
	// Shape is the tunnel's flagged shape (ShapeInteractive,
	// ShapeBulkUpload; "" = none).
	Shape string `json:"shape,omitempty"`
}

// Session is one launch's use of a proxy.
type Session struct {
	Run        string
	lim        EgressLimits
	requireTLS bool
	nonTLS     []string
	nonTLSBy   string
	shape      SessionOpts

	up, down atomic.Int64

	mu     sync.Mutex
	conns  map[*tunnel]struct{}
	closed string // why the session refuses further requests ("" = open)
}

func newSession(o SessionOpts) *Session {
	return &Session{Run: o.Run, lim: o.Limits.resolved(), requireTLS: o.RequireTLS, nonTLS: o.NonTLS, nonTLSBy: o.NonTLSBy, shape: o, conns: map[*tunnel]struct{}{}}
}

// Bytes reports the session's totals.
func (s *Session) Bytes() (up, down int64) { return s.up.Load(), s.down.Load() }

// refused reports why the session takes no further requests ("" = it does).
func (s *Session) refused() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// shut closes every open tunnel and refuses what follows, for why.
func (s *Session) shut(why string) {
	s.mu.Lock()
	if s.closed == "" {
		s.closed = why
	}
	conns := make([]*tunnel, 0, len(s.conns))
	for t := range s.conns {
		conns = append(conns, t)
	}
	s.mu.Unlock()
	for _, t := range conns {
		t.close(why)
	}
}

func (s *Session) add(t *tunnel) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed != "" {
		return false
	}
	s.conns[t] = struct{}{}
	return true
}

func (s *Session) remove(t *tunnel) {
	s.mu.Lock()
	delete(s.conns, t)
	s.mu.Unlock()
}

// count adds n bytes in one direction and enforces the caps.
func (s *Session) count(n int64, upstream bool) {
	if upstream {
		if v := s.up.Add(n); s.lim.Upload > 0 && v > s.lim.Upload {
			s.shut("upload cap reached")
		}
		return
	}
	if v := s.down.Add(n); s.lim.Download > 0 && v > s.lim.Download {
		s.shut("download cap reached")
	}
}

// tunnel is one open CONNECT tunnel.
type tunnel struct {
	sess           *Session
	target         string
	client, server net.Conn
	flagged        sync.Once
	shape          atomic.Value // string
	start          time.Time
	last           atomic.Int64 // unix nanos of the last byte either way
	lastDir        atomic.Int32 // 1 client→server, 2 server→client
	maxIdle        atomic.Int64
	serverFirst    atomic.Int32
	up, down       atomic.Int64
	once           sync.Once
	why            atomic.Value // string
}

func (t *tunnel) close(why string) {
	t.once.Do(func() {
		t.why.Store(why)
		t.client.Close()
		t.server.Close()
	})
}

// touch records activity in a direction and the gap before it.
func (t *tunnel) touch(dir int32) {
	now := time.Now().UnixNano()
	prev := t.last.Swap(now)
	gap := time.Duration(now - prev)
	if gap > time.Duration(t.maxIdle.Load()) {
		t.maxIdle.Store(int64(gap))
	}
	if dir == 2 && gap >= ServerFirstGap && t.lastDir.Load() != 2 {
		if n := t.serverFirst.Add(1); n >= interactiveMinTurns {
			age := time.Since(t.start)
			if total := t.up.Load() + t.down.Load(); age >= interactiveMinAge && total < interactiveMaxBytes {
				t.flag(ShapeInteractive, fmt.Sprintf("open %s, the far end spoke first %d times after a silence, %d bytes in all", age.Round(time.Second), n, total))
			}
		}
	}
	t.lastDir.Store(dir)
}

// flag reports the tunnel's shape, once, and closes it when the session
// says so.
func (t *tunnel) flag(kind, reason string) {
	o := t.sess.shape
	if o.Shape == ShapeOff {
		return
	}
	t.flagged.Do(func() {
		t.shape.Store(kind)
		action := ShapeAlert
		by := ""
		if o.ShapeAllowAll || (t.target != "" && EgressAllowed(o.ShapeAllowed, t.target)) {
			action, by = "allowed", o.ShapeAllowedBy
		} else if o.Shape == ShapeClose {
			action = ShapeClose
		}
		ev := ShapeEvent{Run: t.sess.Run, Target: t.target, Kind: kind, Reason: reason, Action: action, AllowedBy: by,
			Up: t.up.Load(), Down: t.down.Load(), Age: time.Since(t.start), MaxIdle: time.Duration(t.maxIdle.Load()), ServerFirst: int(t.serverFirst.Load())}
		if o.OnShape != nil {
			go o.OnShape(ev)
		}
		if action == ShapeClose {
			go t.close(kind + "-shaped tunnel closed (sandbox.detect.connections: close)")
		}
	})
}

// pipe copies one direction, counting into the session.
func (t *tunnel) pipe(dst io.Writer, src io.Reader, upstream bool) {
	buf := make([]byte, 32<<10)
	dir := int32(2)
	if upstream {
		dir = 1
	}
	for {
		n, err := src.Read(buf)
		if n > 0 {
			t.touch(dir)
			if upstream {
				if v := t.up.Add(int64(n)); v >= bulkUploadMin && v >= bulkUploadRatio*t.down.Load() {
					t.flag(ShapeBulkUpload, fmt.Sprintf("sent %d bytes, received %d", v, t.down.Load()))
				}
			} else {
				t.down.Add(int64(n))
			}
			t.sess.count(int64(n), upstream)
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// run serves the tunnel until either side ends, the idle limit passes, or
// the session is shut; it returns the tunnel's record.
func (t *tunnel) run(clientR *bufio.Reader) ConnRecord {
	t.start = time.Now()
	t.last.Store(t.start.UnixNano())
	done := make(chan struct{}, 2)
	go func() { t.pipe(t.server, clientR, true); closeWrite(t.server); done <- struct{}{} }()
	go func() { t.pipe(t.client, t.server, false); t.close(""); done <- struct{}{} }()
	stop := make(chan struct{})
	if idle := t.sess.lim.Idle; idle > 0 {
		go func() {
			tick := idle / 4
			if tick > 15*time.Second {
				tick = 15 * time.Second
			}
			if tick < 10*time.Millisecond {
				tick = 10 * time.Millisecond
			}
			tk := time.NewTicker(tick)
			defer tk.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tk.C:
					if time.Since(time.Unix(0, t.last.Load())) >= idle {
						t.close("idle timeout")
						return
					}
				}
			}
		}()
	}
	<-done
	t.close("")
	<-done
	close(stop)
	rec := ConnRecord{Run: t.sess.Run, Outcome: "closed", Up: t.up.Load(), Down: t.down.Load(),
		Duration: time.Since(t.start), MaxIdle: time.Duration(t.maxIdle.Load()), ServerFirst: int(t.serverFirst.Load())}
	if sh, _ := t.shape.Load().(string); sh != "" {
		rec.Shape = sh
	}
	if w, _ := t.why.Load().(string); w != "" {
		rec.Reason = w
	}
	return rec
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
