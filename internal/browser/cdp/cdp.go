// Package cdp is a minimal Chrome DevTools Protocol client over the pipe
// transport (`--remote-debugging-pipe`): the browser reads commands on its
// fd 3 and writes replies and events on its fd 4, each message one JSON
// object terminated by a NUL byte. There is no port: nothing but the
// process holding the pipe's other ends can drive the browser.
//
// Sessions are flat (Target.attachToTarget{flatten: true}): a command for a
// page carries that page's sessionId, and so do its events.
package cdp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Event is one protocol event.
type Event struct {
	Method  string
	Session string
	Params  json.RawMessage
}

// Error is a protocol error reply.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("cdp: %s (%s)", e.Message, e.Data)
	}
	return "cdp: " + e.Message
}

// ErrClosed is returned once the browser's end of the pipe is gone.
var ErrClosed = errors.New("cdp: the browser connection is closed")

type message struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *Error          `json:"error,omitempty"`
}

// Conn is one browser connection.
type Conn struct {
	w      io.WriteCloser
	r      io.ReadCloser
	wmu    sync.Mutex
	nextID atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan message
	subs    map[int]func(Event)
	nsub    int

	done chan struct{}
	err  error
}

// New starts a connection: w is the browser's command pipe (its fd 3), r
// its reply pipe (its fd 4).
func New(w io.WriteCloser, r io.ReadCloser) *Conn {
	c := &Conn{w: w, r: r, pending: map[int64]chan message{}, subs: map[int]func(Event){}, done: make(chan struct{})}
	go c.read()
	return c
}

func (c *Conn) read() {
	br := bufio.NewReaderSize(c.r, 1<<20)
	var err error
	for {
		var b []byte
		b, err = br.ReadBytes(0)
		if err != nil {
			break
		}
		var m message
		if json.Unmarshal(b[:len(b)-1], &m) != nil {
			continue
		}
		if m.ID != 0 {
			c.mu.Lock()
			ch := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		if m.Method == "" {
			continue
		}
		c.mu.Lock()
		fns := make([]func(Event), 0, len(c.subs))
		for _, fn := range c.subs {
			fns = append(fns, fn)
		}
		c.mu.Unlock()
		ev := Event{Method: m.Method, Session: m.SessionID, Params: m.Params}
		for _, fn := range fns {
			fn(ev)
		}
	}
	c.mu.Lock()
	c.err = ErrClosed
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.done)
}

// Done is closed when the browser's end of the pipe closes.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Close closes the command pipe (the browser exits on it) and the reader.
func (c *Conn) Close() {
	c.wmu.Lock()
	_ = c.w.Close()
	c.wmu.Unlock()
	_ = c.r.Close()
}

// Listen calls fn for every event until the returned func is called. fn
// runs on the reader goroutine: it must not block, and must not Call.
func (c *Conn) Listen(fn func(Event)) (cancel func()) {
	c.mu.Lock()
	c.nsub++
	id := c.nsub
	c.subs[id] = fn
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.subs, id)
		c.mu.Unlock()
	}
}

// Call sends one command (session "" = the browser itself) and decodes its
// result into result (nil = discard).
func (c *Conn) Call(ctx context.Context, session, method string, params, result any) error {
	id := c.nextID.Add(1)
	m := message{ID: id, Method: method, SessionID: session}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		m.Params = b
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	ch := make(chan message, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.err
	}
	c.pending[id] = ch
	c.mu.Unlock()
	c.wmu.Lock()
	_, err = c.w.Write(append(b, 0))
	c.wmu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("cdp: write %s: %w", method, err)
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("cdp: %s: %w", method, ctx.Err())
	case r, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if r.Error != nil {
			return fmt.Errorf("%s: %w", method, r.Error)
		}
		if result != nil && len(r.Result) > 0 {
			return json.Unmarshal(r.Result, result)
		}
		return nil
	}
}
