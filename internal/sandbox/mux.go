package sandbox

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The container mux: an agent container has no network (`--network none`),
// and a host unix socket bind-mounted into it is not reachable everywhere
// (Docker Desktop's file sharing does not carry connect(2) on macOS). So the
// one channel conductor keeps into a container is the stdio of a process it
// execs there — `conductor sandbox-net --mux`, started by the daemon with
// `docker exec -i` — and every connection that crosses the boundary is a
// stream multiplexed over it:
//
//   - container → host: the egress proxy, a host-loopback model endpoint,
//     the conductor socket. The host asks the container side to LISTEN on a
//     loopback port (or a unix socket in the container's socket dir) for a
//     target the HOST chose; a connection there opens a stream that names
//     only the listener's id, and the host dials that listener's target.
//     The container can never make the host dial anything else.
//   - host → container: a server the agent runs (opencode's), dialed at a
//     loopback address inside the container.
//
// Each stream is flow-controlled (a receive window granted back as it is
// read), so a stalled reader never blocks the others.

const muxVersion = "conductor-mux/1"

// Frame types.
const (
	frHello     byte = 1  // payload: version
	frListen    byte = 2  // host→ctr: muxListenReq (JSON)
	frListening byte = 3  // ctr→host: muxListenResp (JSON)
	frUnlisten  byte = 4  // host→ctr: 4-byte listener id
	frOpen      byte = 5  // host→ctr: dial address; ctr→host: 4-byte listener id
	frOpenOK    byte = 6  // the stream is open
	frOpenFail  byte = 7  // payload: why
	frData      byte = 8  // payload: bytes
	frWindow    byte = 9  // payload: 4-byte credit
	frClose     byte = 10 // the sender will write no more
	frReset     byte = 11 // the stream is gone
)

const (
	muxHeader   = 9
	muxMaxFrame = 64 << 10
	muxMaxData  = 32 << 10
	// muxWindow is each stream's receive window, both directions.
	muxWindow = 256 << 10
	// muxOpenTimeout bounds an open or a listen round trip.
	muxOpenTimeout = 15 * time.Second
)

type muxListenReq struct {
	ID   uint32 `json:"id"`
	Net  string `json:"net"`  // tcp | unix
	Addr string `json:"addr"` // 127.0.0.1:PORT (0 = any free port) | a socket path
}

type muxListenResp struct {
	ID    uint32 `json:"id"`
	Addr  string `json:"addr,omitempty"`
	Error string `json:"error,omitempty"`
}

// Mux is one side of the multiplexed channel.
type Mux struct {
	host bool
	r    *bufio.Reader
	w    io.Writer
	wmu  sync.Mutex
	cl   io.Closer

	mu      sync.Mutex
	streams map[uint32]*muxStream
	nextID  uint32
	opening map[uint32]chan string // stream id → "" (ok) or why it failed

	// host side: what each container listener's connections dial.
	targets    map[uint32]func() (net.Conn, error)
	listenWait map[uint32]chan muxListenResp
	nextLis    uint32

	// container side: open listeners, and the one directory unix ones may
	// live in.
	lns     map[uint32]net.Listener
	sockDir string

	hello chan string
	done  chan struct{}
	once  sync.Once
	err   error
}

func newMux(host bool, r io.Reader, w io.Writer, cl io.Closer) *Mux {
	m := &Mux{
		host: host, r: bufio.NewReaderSize(r, 64<<10), w: w, cl: cl,
		streams: map[uint32]*muxStream{}, opening: map[uint32]chan string{},
		targets: map[uint32]func() (net.Conn, error){}, listenWait: map[uint32]chan muxListenResp{},
		lns:   map[uint32]net.Listener{},
		hello: make(chan string, 1), done: make(chan struct{}),
	}
	if host {
		m.nextID = 1
	} else {
		m.nextID = 2
	}
	return m
}

// NewHostMux speaks the host side over r/w (the exec'd process's stdout and
// stdin) and waits for the container side's hello. cl is closed when the mux
// ends (the process's stdin, so it exits).
func NewHostMux(r io.Reader, w io.Writer, cl io.Closer) (*Mux, error) {
	m := newMux(true, r, w, cl)
	go m.readLoop()
	if err := m.writeFrame(frHello, 0, []byte(muxVersion)); err != nil {
		m.fail(err)
		return nil, err
	}
	select {
	case v := <-m.hello:
		if v != muxVersion {
			err := fmt.Errorf("sandbox mux: the container speaks %q, this conductor %q", v, muxVersion)
			m.fail(err)
			return nil, err
		}
		return m, nil
	case <-m.done:
		return nil, fmt.Errorf("sandbox mux: ended before it started: %w", m.Err())
	case <-time.After(muxOpenTimeout):
		err := errors.New("sandbox mux: no hello from the container")
		m.fail(err)
		return nil, err
	}
}

// ServeContainerMux is the container side (`conductor sandbox-net --mux`):
// it serves until the host goes away. Unix listeners may only be made in
// sockDir.
func ServeContainerMux(r io.Reader, w io.Writer, sockDir string) error {
	m := newMux(false, r, w, nil)
	m.sockDir = filepath.Clean(sockDir)
	if err := m.writeFrame(frHello, 0, []byte(muxVersion)); err != nil {
		return err
	}
	m.readLoop()
	if errors.Is(m.err, io.EOF) {
		return nil
	}
	return m.err
}

// Done is closed when the mux has ended.
func (m *Mux) Done() <-chan struct{} { return m.done }

// Err is why the mux ended (nil while it runs).
func (m *Mux) Err() error {
	select {
	case <-m.done:
		return m.err
	default:
		return nil
	}
}

// Close ends the mux and every stream on it.
func (m *Mux) Close() error {
	m.fail(errors.New("sandbox mux: closed"))
	return nil
}

func (m *Mux) fail(err error) {
	m.once.Do(func() {
		m.mu.Lock()
		m.err = err
		streams := m.streams
		m.streams = map[uint32]*muxStream{}
		lns := m.lns
		m.lns = map[uint32]net.Listener{}
		for id, ch := range m.opening {
			ch <- "mux closed"
			delete(m.opening, id)
		}
		m.mu.Unlock()
		for _, s := range streams {
			s.markReset()
		}
		for _, ln := range lns {
			ln.Close()
		}
		if m.cl != nil {
			m.cl.Close()
		}
		close(m.done)
	})
}

func (m *Mux) writeFrame(typ byte, id uint32, payload []byte) error {
	if len(payload) > muxMaxFrame {
		return fmt.Errorf("sandbox mux: frame of %d bytes", len(payload))
	}
	var hdr [muxHeader]byte
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:5], id)
	binary.BigEndian.PutUint32(hdr[5:9], uint32(len(payload)))
	m.wmu.Lock()
	defer m.wmu.Unlock()
	select {
	case <-m.done:
		return errors.New("sandbox mux: closed")
	default:
	}
	if _, err := m.w.Write(hdr[:]); err != nil {
		go m.fail(err)
		return err
	}
	if len(payload) > 0 {
		if _, err := m.w.Write(payload); err != nil {
			go m.fail(err)
			return err
		}
	}
	return nil
}

func (m *Mux) readLoop() {
	var hdr [muxHeader]byte
	buf := make([]byte, muxMaxFrame)
	for {
		if _, err := io.ReadFull(m.r, hdr[:]); err != nil {
			m.fail(err)
			return
		}
		typ, id, n := hdr[0], binary.BigEndian.Uint32(hdr[1:5]), binary.BigEndian.Uint32(hdr[5:9])
		if n > muxMaxFrame {
			m.fail(fmt.Errorf("sandbox mux: peer sent a %d-byte frame", n))
			return
		}
		p := buf[:n]
		if _, err := io.ReadFull(m.r, p); err != nil {
			m.fail(err)
			return
		}
		if err := m.handle(typ, id, p); err != nil {
			m.fail(err)
			return
		}
	}
}

// handle dispatches one frame. It never blocks on a stream's reader.
func (m *Mux) handle(typ byte, id uint32, p []byte) error {
	switch typ {
	case frHello:
		select {
		case m.hello <- string(p):
		default:
		}
	case frData:
		if s := m.stream(id); s != nil {
			if !s.receive(p) {
				// More than the window granted: a peer not playing by the
				// rules loses the stream, not the mux.
				m.resetStream(id)
			}
		}
	case frWindow:
		if len(p) == 4 {
			if s := m.stream(id); s != nil {
				s.credit(int(binary.BigEndian.Uint32(p)))
			}
		}
	case frClose:
		if s := m.stream(id); s != nil {
			s.markEOF()
		}
	case frReset:
		m.mu.Lock()
		s := m.streams[id]
		delete(m.streams, id)
		m.mu.Unlock()
		if s != nil {
			s.markReset()
		}
	case frOpenOK, frOpenFail:
		m.mu.Lock()
		ch := m.opening[id]
		delete(m.opening, id)
		m.mu.Unlock()
		if ch != nil {
			why := ""
			if typ == frOpenFail {
				why = string(p)
				if why == "" {
					why = "refused"
				}
			}
			ch <- why
		}
	case frOpen:
		if m.host {
			return m.hostAccept(id, p)
		}
		m.containerDial(id, string(p))
	case frListen:
		if m.host {
			return errors.New("sandbox mux: the container may not ask the host to listen")
		}
		var req muxListenReq
		if err := json.Unmarshal(p, &req); err != nil {
			return err
		}
		m.containerListen(req)
	case frListening:
		if !m.host {
			return nil
		}
		var resp muxListenResp
		if err := json.Unmarshal(p, &resp); err != nil {
			return err
		}
		m.mu.Lock()
		ch := m.listenWait[resp.ID]
		delete(m.listenWait, resp.ID)
		m.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	case frUnlisten:
		if !m.host && len(p) == 4 {
			lid := binary.BigEndian.Uint32(p)
			m.mu.Lock()
			ln := m.lns[lid]
			delete(m.lns, lid)
			m.mu.Unlock()
			if ln != nil {
				ln.Close()
			}
		}
	default:
		return fmt.Errorf("sandbox mux: unknown frame type %d", typ)
	}
	return nil
}

func (m *Mux) stream(id uint32) *muxStream {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.streams[id]
}

func (m *Mux) resetStream(id uint32) {
	m.mu.Lock()
	s := m.streams[id]
	delete(m.streams, id)
	m.mu.Unlock()
	if s != nil {
		s.markReset()
	}
	_ = m.writeFrame(frReset, id, nil)
}

// newStream registers a stream; mine: this side opened it.
func (m *Mux) newStream(id uint32) *muxStream {
	s := &muxStream{m: m, id: id, sendCredit: muxWindow}
	s.cond = sync.NewCond(&s.mu)
	m.mu.Lock()
	m.streams[id] = s
	m.mu.Unlock()
	return s
}

// open opens a stream to the peer with payload and waits for its answer.
func (m *Mux) open(payload []byte) (*muxStream, error) {
	m.mu.Lock()
	id := m.nextID
	m.nextID += 2
	ch := make(chan string, 1)
	m.opening[id] = ch
	m.mu.Unlock()
	s := m.newStream(id)
	if err := m.writeFrame(frOpen, id, payload); err != nil {
		m.dropOpen(id)
		return nil, err
	}
	select {
	case why := <-ch:
		if why != "" {
			m.dropOpen(id)
			return nil, fmt.Errorf("sandbox mux: open refused: %s", why)
		}
		return s, nil
	case <-time.After(muxOpenTimeout):
		m.dropOpen(id)
		_ = m.writeFrame(frReset, id, nil)
		return nil, errors.New("sandbox mux: open timed out")
	}
}

func (m *Mux) dropOpen(id uint32) {
	m.mu.Lock()
	delete(m.opening, id)
	s := m.streams[id]
	delete(m.streams, id)
	m.mu.Unlock()
	if s != nil {
		s.markReset()
	}
}

// Dial (host side) connects to addr inside the container: a loopback
// address only.
func (m *Mux) Dial(addr string) (net.Conn, error) {
	if !m.host {
		return nil, errors.New("sandbox mux: only the host dials into the container")
	}
	if !loopbackAddr(addr) {
		return nil, fmt.Errorf("sandbox mux: %s is not a loopback address", addr)
	}
	return m.open([]byte(addr))
}

// Listen (host side) asks the container to listen on netw/addr ("tcp"
// 127.0.0.1:0 for any free port, or "unix" and a path in the container's
// socket dir); each connection there is served by dial, here. It returns
// the address the container listens on and a func that stops it.
func (m *Mux) Listen(netw, addr string, dial func() (net.Conn, error)) (string, func(), error) {
	if !m.host {
		return "", nil, errors.New("sandbox mux: only the host asks for listeners")
	}
	m.mu.Lock()
	m.nextLis++
	lid := m.nextLis
	ch := make(chan muxListenResp, 1)
	m.listenWait[lid] = ch
	m.targets[lid] = dial
	m.mu.Unlock()
	drop := func() {
		m.mu.Lock()
		delete(m.targets, lid)
		delete(m.listenWait, lid)
		m.mu.Unlock()
	}
	b, _ := json.Marshal(muxListenReq{ID: lid, Net: netw, Addr: addr})
	if err := m.writeFrame(frListen, 0, b); err != nil {
		drop()
		return "", nil, err
	}
	select {
	case resp := <-ch:
		if resp.Error != "" {
			drop()
			return "", nil, fmt.Errorf("sandbox mux: listen %s %s in the container: %s", netw, addr, resp.Error)
		}
		var once sync.Once
		return resp.Addr, func() {
			once.Do(func() {
				drop()
				var idb [4]byte
				binary.BigEndian.PutUint32(idb[:], lid)
				_ = m.writeFrame(frUnlisten, 0, idb[:])
			})
		}, nil
	case <-m.done:
		drop()
		return "", nil, fmt.Errorf("sandbox mux: ended: %w", m.Err())
	case <-time.After(muxOpenTimeout):
		drop()
		return "", nil, errors.New("sandbox mux: listen timed out")
	}
}

// hostAccept serves a stream the container opened at one of its listeners:
// the host dials that listener's own target, never one the container names.
func (m *Mux) hostAccept(id uint32, p []byte) error {
	if id%2 != 0 {
		return errors.New("sandbox mux: the container opened a host-numbered stream")
	}
	if len(p) != 4 {
		return errors.New("sandbox mux: malformed open")
	}
	lid := binary.BigEndian.Uint32(p)
	m.mu.Lock()
	dial := m.targets[lid]
	_, dup := m.streams[id]
	m.mu.Unlock()
	if dup {
		return errors.New("sandbox mux: stream id reused")
	}
	if dial == nil {
		_ = m.writeFrame(frOpenFail, id, []byte("no such listener"))
		return nil
	}
	s := m.newStream(id)
	go func() {
		c, err := dial()
		if err != nil {
			m.dropOpen(id)
			_ = m.writeFrame(frOpenFail, id, []byte(err.Error()))
			return
		}
		if err := m.writeFrame(frOpenOK, id, nil); err != nil {
			c.Close()
			return
		}
		pipeConns(c, s)
	}()
	return nil
}

// containerDial serves a stream the host opened to a loopback address in
// the container.
func (m *Mux) containerDial(id uint32, addr string) {
	if id%2 != 1 || !loopbackAddr(addr) {
		_ = m.writeFrame(frOpenFail, id, []byte("not a loopback address"))
		return
	}
	s := m.newStream(id)
	go func() {
		c, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			m.dropOpen(id)
			_ = m.writeFrame(frOpenFail, id, []byte(err.Error()))
			return
		}
		if err := m.writeFrame(frOpenOK, id, nil); err != nil {
			c.Close()
			return
		}
		pipeConns(c, s)
	}()
}

// containerListen opens a listener the host asked for.
func (m *Mux) containerListen(req muxListenReq) {
	answer := func(addr string, err error) {
		resp := muxListenResp{ID: req.ID, Addr: addr}
		if err != nil {
			resp.Error = err.Error()
		}
		b, _ := json.Marshal(resp)
		_ = m.writeFrame(frListening, 0, b)
	}
	var ln net.Listener
	var err error
	switch req.Net {
	case "tcp":
		if !loopbackAddr(req.Addr) {
			answer("", errors.New("not a loopback address"))
			return
		}
		ln, err = listenRetry("tcp", req.Addr)
	case "unix":
		p := filepath.Clean(req.Addr)
		if m.sockDir == "" || m.sockDir == "." || filepath.Dir(p) != m.sockDir {
			answer("", fmt.Errorf("unix listeners live in %s", m.sockDir))
			return
		}
		_ = os.Remove(p) // a socket left by an earlier mux in this container
		ln, err = net.Listen("unix", p)
		if err == nil {
			_ = os.Chmod(p, 0o600)
		}
	default:
		err = fmt.Errorf("unknown network %q", req.Net)
	}
	if err != nil {
		answer("", err)
		return
	}
	m.mu.Lock()
	m.lns[req.ID] = ln
	m.mu.Unlock()
	answer(ln.Addr().String(), nil)
	go func() {
		var idb [4]byte
		binary.BigEndian.PutUint32(idb[:], req.ID)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				s, err := m.open(idb[:])
				if err != nil {
					c.Close()
					return
				}
				pipeConns(c, s)
			}()
		}
	}()
}

// listenRetry listens on a fixed port, waiting out an earlier mux in the
// same container that has not let go of it yet.
func listenRetry(netw, addr string) (net.Listener, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		ln, err := net.Listen(netw, addr)
		if err == nil || time.Now().After(deadline) || strings.HasSuffix(addr, ":0") {
			return ln, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// loopbackAddr reports a host:port on a loopback address.
func loopbackAddr(addr string) bool {
	h, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return false
	}
	ip := net.ParseIP(h)
	return h == "localhost" || (ip != nil && ip.IsLoopback())
}

// pipeConns copies both ways until both sides are done (or either fails).
func pipeConns(a, b net.Conn) {
	type res struct{ err error }
	done := make(chan res, 2)
	cp := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if err == nil {
			if cw, ok := dst.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
		}
		done <- res{err}
	}
	go cp(a, b)
	go cp(b, a)
	if r := <-done; r.err != nil {
		a.Close()
		b.Close()
	}
	<-done
	a.Close()
	b.Close()
}

// muxStream is one connection over the mux: a net.Conn.
type muxStream struct {
	m  *Mux
	id uint32

	mu         sync.Mutex
	cond       *sync.Cond
	buf        []byte
	eof        bool // the peer will write no more
	reset      bool // the stream is gone
	wclosed    bool // we will write no more
	closed     bool // Close was called
	sendCredit int
	unacked    int
	rdl, wdl   time.Time
	timer      *time.Timer
}

// receive buffers data; false if the peer overran its window.
func (s *muxStream) receive(p []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reset || s.closed {
		return true
	}
	if len(s.buf)+len(p) > muxWindow {
		return false
	}
	s.buf = append(s.buf, p...)
	s.cond.Broadcast()
	return true
}

func (s *muxStream) credit(n int) {
	s.mu.Lock()
	if n > 0 && s.sendCredit+n <= 2*muxWindow {
		s.sendCredit += n
	}
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *muxStream) markEOF() {
	s.mu.Lock()
	s.eof = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *muxStream) markReset() {
	s.mu.Lock()
	s.reset = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *muxStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	for len(s.buf) == 0 {
		switch {
		case s.closed:
			s.mu.Unlock()
			return 0, net.ErrClosed
		case s.eof || s.reset:
			s.mu.Unlock()
			return 0, io.EOF
		case !s.rdl.IsZero() && !time.Now().Before(s.rdl):
			s.mu.Unlock()
			return 0, os.ErrDeadlineExceeded
		}
		s.cond.Wait()
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	if len(s.buf) == 0 {
		s.buf = nil
	}
	s.unacked += n
	grant := 0
	if s.unacked >= muxWindow/4 && !s.reset {
		grant, s.unacked = s.unacked, 0
	}
	s.mu.Unlock()
	if grant > 0 {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(grant))
		_ = s.m.writeFrame(frWindow, s.id, b[:])
	}
	return n, nil
}

func (s *muxStream) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		s.mu.Lock()
		for s.sendCredit == 0 && !s.reset && !s.wclosed && !s.closed && (s.wdl.IsZero() || time.Now().Before(s.wdl)) {
			s.cond.Wait()
		}
		switch {
		case s.reset || s.wclosed || s.closed:
			s.mu.Unlock()
			return written, io.ErrClosedPipe
		case s.sendCredit == 0:
			s.mu.Unlock()
			return written, os.ErrDeadlineExceeded
		}
		n := len(p) - written
		if n > s.sendCredit {
			n = s.sendCredit
		}
		if n > muxMaxData {
			n = muxMaxData
		}
		s.sendCredit -= n
		s.mu.Unlock()
		if err := s.m.writeFrame(frData, s.id, p[written:written+n]); err != nil {
			return written, err
		}
		written += n
	}
	return written, nil
}

// CloseWrite tells the peer this side will write no more.
func (s *muxStream) CloseWrite() error {
	s.mu.Lock()
	if s.wclosed || s.reset {
		s.mu.Unlock()
		return nil
	}
	s.wclosed = true
	s.cond.Broadcast()
	s.mu.Unlock()
	return s.m.writeFrame(frClose, s.id, nil)
}

func (s *muxStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	wasReset := s.reset
	if s.timer != nil {
		s.timer.Stop()
	}
	s.cond.Broadcast()
	s.mu.Unlock()
	s.m.mu.Lock()
	delete(s.m.streams, s.id)
	s.m.mu.Unlock()
	if !wasReset {
		_ = s.m.writeFrame(frReset, s.id, nil)
	}
	return nil
}

type muxAddr struct{}

func (muxAddr) Network() string { return "conductor-mux" }
func (muxAddr) String() string  { return "conductor-mux" }

func (s *muxStream) LocalAddr() net.Addr  { return muxAddr{} }
func (s *muxStream) RemoteAddr() net.Addr { return muxAddr{} }

func (s *muxStream) SetDeadline(t time.Time) error {
	s.setDeadlines(t, true, true)
	return nil
}

func (s *muxStream) SetReadDeadline(t time.Time) error {
	s.setDeadlines(t, true, false)
	return nil
}

func (s *muxStream) SetWriteDeadline(t time.Time) error {
	s.setDeadlines(t, false, true)
	return nil
}

func (s *muxStream) setDeadlines(t time.Time, r, w bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r {
		s.rdl = t
	}
	if w {
		s.wdl = t
	}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if !t.IsZero() {
		s.timer = time.AfterFunc(time.Until(t), func() {
			s.mu.Lock()
			s.cond.Broadcast()
			s.mu.Unlock()
		})
	}
	s.cond.Broadcast()
}
