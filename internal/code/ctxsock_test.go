package code

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The socket in front of CtxHandler. Two walls are under test here — a 0700
// directory nobody else can enter, and a per-run token nobody else has — plus
// the lifetime promise that neither outlives the step.

// dialCtx sends one request to a server and returns its response. It is the
// protocol's client half written out longhand (rather than through
// ctxRoundTrip) so a change to the reference client can't quietly redefine
// what these tests are asserting about the wire.
func dialCtx(t *testing.T, sock string, req CtxRequest) CtxResponse {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial %s: %v", sock, err)
	}
	defer c.Close()
	if err := json.NewEncoder(c).Encode(req); err != nil {
		t.Fatal(err)
	}
	var res CtxResponse
	if err := json.NewDecoder(c).Decode(&res); err != nil {
		t.Fatal(err)
	}
	return res
}

func startTestCtxServer(t *testing.T, guard DataGuard) *ctxServer {
	t.Helper()
	s, err := startCtxServer(CtxHandler{Guard: guard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The token authenticates EVERY request: the right one works, a wrong or
// absent one is refused, and the refusal happens before the guard or the
// store is consulted.
func TestCtxSockToken(t *testing.T) {
	st := tempKV(t)
	touched := false
	s := startTestCtxServer(t, func(kind, op, resource string, args []any) error {
		touched = true
		return nil
	})

	set := CtxRequest{Kind: CtxKindKV, Op: "set", Resource: "s", Args: []any{"ns", "k", "v"}}

	for _, tok := range []string{"", "not-the-token", s.token + "x", "0" + s.token} {
		bad := set
		bad.Token = tok
		res := dialCtx(t, s.path, bad)
		if res.OK || !res.Refused || !strings.Contains(res.Error, "token") {
			t.Fatalf("token %q was accepted: %#v", tok, res)
		}
	}
	if touched {
		t.Error("an unauthenticated request reached the DataGuard")
	}
	if _, found, _ := st.Get("ns", "k"); found {
		t.Error("an unauthenticated request reached the store")
	}

	good := set
	good.Token = s.token
	if res := dialCtx(t, s.path, good); !res.OK {
		t.Fatalf("the run's own token was refused: %#v", res)
	}
	if v, found, _ := st.Get("ns", "k"); !found || v != "v" {
		t.Fatalf("authenticated write did not land: %v", v)
	}
}

// Cross-run isolation: two runs, two sockets, two tokens, and neither
// capability names the other's data plane. This is the whole point of
// minting per run rather than per daemon.
func TestCtxSockCrossRunIsolation(t *testing.T) {
	tempKV(t)
	a := startTestCtxServer(t, nil)
	b := startTestCtxServer(t, nil)

	if a.path == b.path || a.token == b.token || filepath.Dir(a.path) == filepath.Dir(b.path) {
		t.Fatal("two runs shared a socket, a directory or a token")
	}
	get := CtxRequest{Kind: CtxKindKV, Op: "get", Resource: "s", Args: []any{"ns", "k"}}

	bAtA := get
	bAtA.Token = b.token
	if res := dialCtx(t, a.path, bAtA); res.OK || !res.Refused {
		t.Fatalf("run B's token opened run A's data plane: %#v", res)
	}
	aAtB := get
	aAtB.Token = a.token
	if res := dialCtx(t, b.path, aAtB); res.OK || !res.Refused {
		t.Fatalf("run A's token opened run B's data plane: %#v", res)
	}
	// Each still works with its own.
	own := get
	own.Token = a.token
	if res := dialCtx(t, a.path, own); !res.OK {
		t.Fatalf("run A's own token: %#v", res)
	}
	// Closing A leaves B entirely alone — teardown is per run too.
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	own.Token = b.token
	if res := dialCtx(t, b.path, own); !res.OK {
		t.Fatalf("closing run A disturbed run B: %#v", res)
	}
}

// The socket's directory is private to the daemon's uid, and so is the
// socket inode: the token is the second wall, not the only one.
func TestCtxSockDirIsPrivate(t *testing.T) {
	s := startTestCtxServer(t, nil)
	dir := filepath.Dir(s.path)
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o700 {
		t.Fatalf("socket directory mode = %#o, want 0700", mode)
	}
	sfi, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := sfi.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("socket mode = %#o, want owner-only", mode)
	}
}

// Close is idempotent and takes the directory with it, so a descriptor or a
// path leaked to a surviving grandchild resolves to nothing.
func TestCtxSockCloseRemovesEverything(t *testing.T) {
	s := startTestCtxServer(t, nil)
	dir := filepath.Dir(s.path)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("socket directory survived Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := net.Dial("unix", s.path); err == nil {
		t.Fatal("the socket still accepts connections after Close")
	}
}

// One connection carries many request/response pairs, in order, and many
// connections may be open at once — the shape a step with a loop produces.
func TestCtxSockStreamAndConcurrency(t *testing.T) {
	tempKV(t)
	s := startTestCtxServer(t, nil)

	c, err := net.Dial("unix", s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	enc, dec := json.NewEncoder(c), json.NewDecoder(c)
	for i, req := range []CtxRequest{
		{Token: s.token, Kind: CtxKindKV, Op: "set", Resource: "s", Args: []any{"ns", "seq", "one"}},
		{Token: s.token, Kind: CtxKindKV, Op: "get", Resource: "s", Args: []any{"ns", "seq"}},
		{Token: "wrong", Kind: CtxKindKV, Op: "get", Resource: "s", Args: []any{"ns", "seq"}},
	} {
		if err := enc.Encode(req); err != nil {
			t.Fatal(err)
		}
		var res CtxResponse
		if err := dec.Decode(&res); err != nil {
			t.Fatal(err)
		}
		switch i {
		case 1:
			if res.Value != "one" {
				t.Fatalf("pipelined get = %#v", res)
			}
		case 2:
			if res.OK {
				t.Fatal("a bad token mid-stream was accepted")
			}
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// ctxRoundTrip, not dialCtx: t.Fatal is not for a helper
			// goroutine, and this half of the test is about the server
			// surviving concurrent dials rather than about the wire.
			res, err := ctxRoundTrip(s.path, CtxRequest{Token: s.token, Kind: CtxKindKV,
				Op: "incr", Resource: "s", Args: []any{"ns", "hits", 1}})
			if err != nil || !res.OK {
				t.Errorf("concurrent incr: %v %#v", err, res)
			}
		}()
	}
	wg.Wait()
}

// A malformed line is answered once and then the connection goes: the
// stream's framing is no longer trustworthy, so the server does not guess.
func TestCtxSockMalformedRequest(t *testing.T) {
	s := startTestCtxServer(t, nil)
	c, err := net.Dial("unix", s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("this is not json\n")); err != nil {
		t.Fatal(err)
	}
	var res CtxResponse
	if err := json.NewDecoder(c).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res.OK || !strings.Contains(res.Error, "bad request") {
		t.Fatalf("res = %#v", res)
	}
}

// The environment handed to the child names the socket and the token, and
// points at conductor's own binary for the reference client.
func TestCtxSockEnv(t *testing.T) {
	s := startTestCtxServer(t, nil)
	env := map[string]string{}
	for _, kv := range s.env("/usr/local/bin/conductor") {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	if env["CONDUCTOR_CTX_SOCK"] != s.path || env["CONDUCTOR_CTX_TOKEN"] != s.token ||
		env["CONDUCTOR_CTX_HELPER"] != "/usr/local/bin/conductor" {
		t.Fatalf("env = %#v", env)
	}
	if len(s.token) < 32 {
		t.Fatalf("token is too short to be unguessable: %q", s.token)
	}
	// No helper resolvable is not fatal — the protocol is still reachable.
	for _, kv := range s.env("") {
		if strings.HasPrefix(kv, "CONDUCTOR_CTX_HELPER=") {
			t.Fatalf("empty helper was exported anyway: %q", kv)
		}
	}
}
