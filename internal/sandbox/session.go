package sandbox

import (
	"bufio"
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
	Run      string        `json:"run,omitempty"`
	Target   string        `json:"target"`
	SNI      string        `json:"sni,omitempty"`
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
}

// Session is one launch's use of a proxy.
type Session struct {
	Run        string
	lim        EgressLimits
	requireTLS bool
	nonTLS     []string
	nonTLSBy   string

	up, down atomic.Int64

	mu     sync.Mutex
	conns  map[*tunnel]struct{}
	closed string // why the session refuses further requests ("" = open)
}

func newSession(o SessionOpts) *Session {
	return &Session{Run: o.Run, lim: o.Limits.resolved(), requireTLS: o.RequireTLS, nonTLS: o.NonTLS, nonTLSBy: o.NonTLSBy, conns: map[*tunnel]struct{}{}}
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
	client, server net.Conn
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
	if dir == 2 && gap >= 30*time.Second && t.lastDir.Load() != 2 {
		t.serverFirst.Add(1)
	}
	t.lastDir.Store(dir)
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
				t.up.Add(int64(n))
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
