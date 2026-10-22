package browser

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// The container mux: a browser in a container is reached over the engine's
// stdin/stdout alone (no port, no bind mount — Docker Desktop's and colima's
// VMs share neither host sockets nor fds). The in-container shim
// (containershim/) speaks the same frames:
//
//	type(1) channel(4) length(4) payload
//
// Channel 0 is Chromium's DevTools pipe. The shim opens a channel per
// connection Chromium makes to its proxy address, which the host side pipes
// into conductor's egress proxy. Channel 1 carries file transfers into and
// out of the container's transfer directory.
const (
	frameData  = 1
	frameOpen  = 2 // shim → host: a proxy connection on this channel
	frameClose = 3
	frameFile  = 4 // host → shim on channel 1: a JSON file request
)

const (
	chanCDP  = 0
	chanFile = 1
)

// Mux is the host side.
type Mux struct {
	w   io.Writer
	wmu sync.Mutex

	// fileMu serializes this browser's file transfers — one at a time, but
	// per-Mux, so a stall on one container never blocks another's I/O.
	fileMu sync.Mutex

	mu    sync.Mutex
	chans map[uint32]*muxChan
	// dial opens a connection to the egress proxy for a new proxy channel.
	dial func() (net.Conn, error)

	cdpR *io.PipeReader
	cdpW *io.PipeWriter
	done chan struct{}
}

type muxChan struct {
	// cmu guards conn and pending: data that arrives before the proxy dial
	// completes is queued, then flushed in order.
	cmu     sync.Mutex
	conn    net.Conn
	pending [][]byte
	closed  bool
	// file transfer replies: data, then one status message, then close.
	file chan fileMsg
}

type fileMsg struct {
	status bool
	p      []byte
}

// NewMux starts the host side over the shim's stdout (r) and stdin (w).
func NewMux(r io.Reader, w io.Writer, dial func() (net.Conn, error)) *Mux {
	m := &Mux{w: w, chans: map[uint32]*muxChan{}, dial: dial, done: make(chan struct{})}
	m.cdpR, m.cdpW = io.Pipe()
	go m.read(bufio.NewReaderSize(r, 1<<20))
	return m
}

// Done is closed when the shim's stream ends.
func (m *Mux) Done() <-chan struct{} { return m.done }

func (m *Mux) send(typ byte, ch uint32, p []byte) error {
	var h [9]byte
	h[0] = typ
	binary.BigEndian.PutUint32(h[1:5], ch)
	binary.BigEndian.PutUint32(h[5:9], uint32(len(p)))
	m.wmu.Lock()
	defer m.wmu.Unlock()
	if _, err := m.w.Write(h[:]); err != nil {
		return err
	}
	_, err := m.w.Write(p)
	return err
}

func (m *Mux) read(r *bufio.Reader) {
	defer close(m.done)
	defer m.cdpW.CloseWithError(io.EOF)
	var h [9]byte
	for {
		if _, err := io.ReadFull(r, h[:]); err != nil {
			m.closeAll()
			return
		}
		typ, ch, n := h[0], binary.BigEndian.Uint32(h[1:5]), binary.BigEndian.Uint32(h[5:9])
		if n > 16<<20 {
			m.closeAll()
			return
		}
		p := make([]byte, n)
		if _, err := io.ReadFull(r, p); err != nil {
			m.closeAll()
			return
		}
		switch {
		case ch == chanCDP && typ == frameData:
			if _, err := m.cdpW.Write(p); err != nil {
				return
			}
		case ch == chanFile:
			m.mu.Lock()
			c := m.chans[chanFile]
			m.mu.Unlock()
			if c == nil {
				continue
			}
			c.cmu.Lock()
			f := c.file
			if typ == frameClose {
				c.file = nil
			}
			c.cmu.Unlock()
			switch {
			case f == nil:
			case typ == frameClose:
				close(f)
			case typ == frameFile:
				f <- fileMsg{status: true, p: p}
			default:
				f <- fileMsg{p: p}
			}
		case typ == frameOpen && ch > chanFile:
			// Registered now, before the dial: the data right behind this
			// frame must not be lost.
			c := &muxChan{}
			m.mu.Lock()
			m.chans[ch] = c
			m.mu.Unlock()
			go m.openProxy(ch, c)
		case typ == frameData:
			m.mu.Lock()
			c := m.chans[ch]
			m.mu.Unlock()
			if c != nil {
				c.cmu.Lock()
				if c.conn == nil {
					c.pending = append(c.pending, p)
					c.cmu.Unlock()
					continue
				}
				conn := c.conn
				c.cmu.Unlock()
				_, _ = conn.Write(p)
			}
		case typ == frameClose:
			m.mu.Lock()
			c := m.chans[ch]
			delete(m.chans, ch)
			m.mu.Unlock()
			if c != nil {
				c.cmu.Lock()
				c.closed = true
				conn := c.conn
				c.cmu.Unlock()
				if conn != nil {
					conn.Close()
				}
			}
		}
	}
}

// openProxy pipes a shim proxy channel into the egress proxy.
func (m *Mux) openProxy(ch uint32, c *muxChan) {
	conn, err := m.dial()
	if err != nil {
		m.mu.Lock()
		delete(m.chans, ch)
		m.mu.Unlock()
		_ = m.send(frameClose, ch, nil)
		return
	}
	c.cmu.Lock()
	for _, p := range c.pending {
		if _, err := conn.Write(p); err != nil {
			break
		}
	}
	c.pending = nil
	c.conn = conn
	closed := c.closed
	c.cmu.Unlock()
	if closed {
		conn.Close()
		return
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if m.send(frameData, ch, buf[:n]) != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	m.mu.Lock()
	_, live := m.chans[ch]
	delete(m.chans, ch)
	m.mu.Unlock()
	conn.Close()
	if live {
		_ = m.send(frameClose, ch, nil)
	}
}

func (m *Mux) closeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch, c := range m.chans {
		c.cmu.Lock()
		if c.conn != nil {
			c.conn.Close()
		}
		if c.file != nil {
			close(c.file)
			c.file = nil
		}
		c.closed = true
		c.cmu.Unlock()
		delete(m.chans, ch)
	}
}

// CDP is the DevTools pipe: write commands, read replies (NUL-delimited).
func (m *Mux) CDP() (io.WriteCloser, io.ReadCloser) { return cdpWriter{m}, m.cdpR }

type cdpWriter struct{ m *Mux }

func (c cdpWriter) Write(p []byte) (int, error) {
	if err := c.m.send(frameData, chanCDP, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c cdpWriter) Close() error { return c.m.send(frameClose, chanCDP, nil) }

// fileReq is one transfer request (the shim confines the path).
type fileReq struct {
	Op   string `json:"op"` // get | put
	Path string `json:"path"`
	Max  int64  `json:"max,omitempty"`
}

// fileXferTimeout bounds one file transfer even when the caller passes no
// deadline: a shim that stays alive but stops answering can never wedge a
// transfer (or the lock behind it) forever.
const fileXferTimeout = 2 * time.Minute

// Get copies a file out of the container's transfer directory (at most max
// bytes) to w.
func (m *Mux) Get(ctx context.Context, path string, max int64, w io.Writer) (int64, error) {
	return m.transfer(ctx, fileReq{Op: "get", Path: path, Max: max}, nil, w)
}

// Put copies r into the container's transfer directory at path.
func (m *Mux) Put(ctx context.Context, path string, r io.Reader) error {
	_, err := m.transfer(ctx, fileReq{Op: "put", Path: path}, r, nil)
	return err
}

func (m *Mux) transfer(ctx context.Context, req fileReq, in io.Reader, out io.Writer) (int64, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, fileXferTimeout)
		defer cancel()
	}
	m.fileMu.Lock()
	defer m.fileMu.Unlock()
	replies := make(chan fileMsg, 64)
	c := &muxChan{file: replies}
	m.mu.Lock()
	m.chans[chanFile] = c
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.chans, chanFile)
		m.mu.Unlock()
	}()
	b, _ := json.Marshal(req)
	if err := m.send(frameFile, chanFile, b); err != nil {
		return 0, err
	}
	if in != nil {
		buf := make([]byte, 32<<10)
		for {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			n, err := in.Read(buf)
			if n > 0 {
				if err := m.send(frameData, chanFile, buf[:n]); err != nil {
					return 0, err
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return 0, err
			}
		}
		if err := m.send(frameClose, chanFile, nil); err != nil {
			return 0, err
		}
	}
	// The reply: data frames, then a status frame ("ok" / "error: …"), then
	// close. A shim that stalls mid-reply (no EOF, no error) never closes
	// replies, so the loop also watches ctx — the transfer unblocks on the
	// deadline instead of holding fileMu forever.
	var total int64
	var status string
	for {
		var msg fileMsg
		var ok bool
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case msg, ok = <-replies:
		}
		if !ok {
			break
		}
		if msg.status {
			status = string(msg.p)
			continue
		}
		p := msg.p
		if out != nil {
			total += int64(len(p))
			if req.Max > 0 && total > req.Max {
				return total, fmt.Errorf("over the size limit (%d bytes)", req.Max)
			}
			if _, err := out.Write(p); err != nil {
				return total, err
			}
		}
	}
	if status != "ok" {
		if status == "" {
			status = "the container closed the transfer"
		}
		return total, fmt.Errorf("container transfer: %s", status)
	}
	return total, nil
}
