package code

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
)

// The per-run ctx SOCKET: the transport that puts CtxHandler in front of a
// `cli` step's subprocess.
//
// # Wire protocol
//
// A unix stream socket speaking JSON Lines in both directions: one JSON
// request object per line, one JSON response object per line, responses in
// request order. A connection may carry many request/response pairs, and a
// step may open as many connections as it likes — each request stands alone.
//
//	--> {"token":"…","kind":"kv","op":"set","resource":"cache","args":["ns","k",{"v":1}]}
//	<-- {"ok":true}
//	--> {"token":"…","kind":"kv","op":"get","resource":"cache","args":["ns","k"]}
//	<-- {"ok":true,"value":{"v":1}}
//	--> {"token":"wrong","kind":"kv","op":"get","resource":"cache","args":["ns","k"]}
//	<-- {"ok":false,"error":"ctx: bad or missing token","refused":true}
//
// Request fields are CtxRequest, response fields CtxResponse (ctxhost.go);
// `args` follows kvInvoke's positional convention so the socket face and the
// in-process `ctx.store(…)` face mean the same thing by the same call. The
// decoder accepts any whitespace between objects, so a client that pretty-
// prints its JSON still works — "one per line" is the contract a client
// should WRITE to, not a parser restriction it can trip over.
//
// # Auth model
//
// The capability is the (socket, token) pair, minted per run and carried to
// the child in its environment:
//
//	CONDUCTOR_CTX_SOCK    the unix socket path
//	CONDUCTOR_CTX_TOKEN   a 256-bit random token, required on EVERY request
//	CONDUCTOR_CTX_HELPER  conductor's own binary — `$HELPER ctx …` is the
//	                      reference client (ctxclient.go)
//
// Two independent walls, because either alone has a hole. The socket lives
// in a fresh 0700 directory with a random name, so another user on the box
// cannot connect at all; the token means that even a process that somehow
// reaches the socket (a same-uid neighbor, a stale descriptor, a sibling
// step that guessed a path) still cannot USE it. A request with a wrong or
// absent token is refused without ever reaching CtxHandler — the guard is
// not even consulted, because there is no authenticated caller to guard.
//
// Both are per RUN. Run B's token presented to run A's socket is simply a
// wrong token: there is no shared secret, no daemon-wide credential, and no
// way for one step's capability to name another step's data plane.
//
// # Lifetime
//
// The server outlives nothing. execCLILocal starts it before the child and
// `defer`s Close, so every exit path of that function — clean exit, non-zero
// exit, context timeout, cancellation, a marshal error before the child even
// starts — closes the listener, unlinks the socket directory, drops every
// open connection and returns only once no request is still in flight. A
// subprocess that outlives its step (a backgrounded grandchild holding the
// inherited env) finds a socket path that no longer exists.
//
// Opt-in from the command's side: a `cli` step that never reads
// CONDUCTOR_CTX_SOCK behaves exactly as it did before this existed. The
// socket costs a listener and a goroutine, and grants nothing until someone
// dials it.

// ctxTokenBytes is the token's entropy. 32 bytes is far past guessing, and
// the token only ever travels between conductor and a child it spawned.
const ctxTokenBytes = 32

// ctxServer is one run's data-plane endpoint.
type ctxServer struct {
	dir   string // the private 0700 directory holding the socket
	path  string // the socket path itself
	token string // the per-run capability token
	h     CtxHandler
	ln    net.Listener

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// startCtxServer mints a run's socket + token and begins serving h. The
// caller MUST Close it (see the lifetime note above).
func startCtxServer(h CtxHandler) (*ctxServer, error) {
	// 0700 by construction (os.MkdirTemp's mode), with a random name: the
	// directory is the first of the two walls.
	dir, err := os.MkdirTemp("", "conductor-ctx-*")
	if err != nil {
		return nil, fmt.Errorf("code: ctx socket: temp dir: %w", err)
	}
	tok := make([]byte, ctxTokenBytes)
	if _, err := rand.Read(tok); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("code: ctx socket: token: %w", err)
	}
	path := filepath.Join(dir, "ctx.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("code: ctx socket: listen: %w", err)
	}
	// Belt to the directory's braces: the socket inode itself is owner-only,
	// so it stays unreachable even if the directory's mode is ever widened.
	_ = os.Chmod(path, 0o600)

	s := &ctxServer{
		dir: dir, path: path, token: hex.EncodeToString(tok),
		h: h, ln: ln, conns: map[net.Conn]struct{}{},
	}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// env is what the child needs to reach this server, in os/exec "K=V" form.
// helper is conductor's own executable path (empty when it can't be
// determined, in which case the variable is simply absent and the wire
// protocol is still there for a client that speaks it directly).
func (s *ctxServer) env(helper string) []string {
	out := []string{
		"CONDUCTOR_CTX_SOCK=" + s.path,
		"CONDUCTOR_CTX_TOKEN=" + s.token,
	}
	if helper != "" {
		out = append(out, "CONDUCTOR_CTX_HELPER="+helper)
	}
	return out
}

// conductorBinary is the path a child should run to get the reference
// client (`$CONDUCTOR_CTX_HELPER ctx kv …` — see ctxclient.go). Empty when
// the executable can't be resolved, which only drops the convenience: the
// socket and token are still there for a step that speaks the protocol
// itself.
func conductorBinary() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}

// serve accepts connections until the listener is closed.
func (s *ctxServer) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return // listener closed (Close) or unrecoverable
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			c.Close()
			return
		}
		s.conns[c] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.handle(c)
	}
}

// handle runs one connection's request/response stream.
func (s *ctxServer) handle(c net.Conn) {
	defer func() {
		c.Close()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		s.wg.Done()
	}()
	dec := json.NewDecoder(c)
	enc := json.NewEncoder(c)
	for {
		var req CtxRequest
		if err := dec.Decode(&req); err != nil {
			if !errors.Is(err, io.EOF) {
				// A malformed request gets one answer and then the
				// connection goes: the stream's framing is no longer
				// trustworthy, so continuing would answer the wrong
				// question.
				_ = enc.Encode(ctxErr(fmt.Errorf("ctx: bad request: %w", err)))
			}
			return
		}
		if err := enc.Encode(s.answer(req)); err != nil {
			return // client hung up, or Close dropped the conn
		}
	}
}

// answer authenticates a request and, if it is genuinely from this run,
// hands it to the handler. Authentication is the transport's job and happens
// FIRST: an unauthenticated caller has no policy to evaluate it against, so
// the guard is never consulted and no store is ever touched.
func (s *ctxServer) answer(req CtxRequest) CtxResponse {
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.token)) != 1 {
		return CtxResponse{OK: false, Refused: true, Error: "ctx: bad or missing token"}
	}
	return s.h.Invoke(req)
}

// Close tears the endpoint down. Safe to call more than once, and ordered so
// that the filesystem artifact is gone before we wait on anything: the
// listener stops first (no new connections), the directory is unlinked (the
// path a leaked child might still hold is now dead), every open connection
// is dropped, and only then do we wait for in-flight ops — which are bounded
// by the stores they are already inside, and which we would not want to
// abandon mid-write anyway.
func (s *ctxServer) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	_ = s.ln.Close()
	err := os.RemoveAll(s.dir)
	for _, c := range conns {
		_ = c.Close()
	}
	s.wg.Wait()
	if err != nil {
		return fmt.Errorf("code: ctx socket: remove %s: %w", s.dir, err)
	}
	return nil
}
