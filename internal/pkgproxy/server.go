// Package pkgproxy is conductor's package proxy: a caching dependency mirror
// conductor serves to its sandboxed agents, the way it already acts for them
// on GitHub. An agent's sandbox reaches no package host; its package manager
// is pointed at this server (relayed into the sandbox like a host-loopback
// model endpoint), and conductor fetches upstream with its own network.
//
// The agent chooses only WHAT to fetch — a module path and version, checked
// against the ecosystem's own grammar and re-encoded — never WHERE: every
// upstream is fixed configuration, so the proxy is no SSRF gateway. Content
// is verified before it is cached (Go: the checksum database), so one agent
// cannot poison what the next is served. Each upstream fetch is reported
// (Event) with the identity and dispatch that asked.
//
// Ecosystems sit behind one interface (Ecosystem): a URL namespace under the
// server and the environment that points an agent's tool at it. Go is the
// first; npm, PyPI and cargo fit the same shape.
package pkgproxy

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Requester is who asked: the sandboxed agent's identity and, for a
// dispatch's agent, the dispatch.
type Requester struct {
	Identity string
	Dispatch string
}

// Event is one audited proxy action: an upstream fetch, or a refusal.
type Event struct {
	Requester Requester
	Ecosystem string
	Module    string
	Version   string
	Artifact  string // list | latest | info | mod | zip | sumdb
	Upstream  bool   // fetched from upstream (not served from the cache)
	Status    int    // the HTTP status the agent got
	Err       string // why it was refused or failed
}

// Ecosystem is one package manager the proxy serves under /<token>/<Name>/.
type Ecosystem interface {
	// Name is the URL segment ("go").
	Name() string
	// Serve answers one request; rest is the path below the ecosystem's
	// segment, still escaped as the client sent it.
	Serve(w http.ResponseWriter, r *http.Request, rest string, c Requester, emit func(Event))
	// AgentEnv points the ecosystem's tool at base (the ecosystem's URL,
	// carrying the caller's token).
	AgentEnv(base string) []string
}

// Server is the package proxy's HTTP listener: loopback only, every request
// carrying a token that names its caller.
type Server struct {
	// OnEvent receives every audited Event (nil: none).
	OnEvent func(Event)

	key  []byte
	ecos map[string]Ecosystem
	ord  []string

	mu  sync.Mutex
	ln  net.Listener
	srv *http.Server
}

// New builds a server signing caller tokens with key (at least 32 bytes).
func New(key []byte, ecos ...Ecosystem) (*Server, error) {
	if len(key) < 32 {
		return nil, errors.New("pkgproxy: token key too short")
	}
	s := &Server{key: append([]byte(nil), key...), ecos: map[string]Ecosystem{}}
	for _, e := range ecos {
		if _, dup := s.ecos[e.Name()]; dup {
			return nil, fmt.Errorf("pkgproxy: ecosystem %s twice", e.Name())
		}
		s.ecos[e.Name()] = e
		s.ord = append(s.ord, e.Name())
	}
	return s, nil
}

// Listen serves on addr, a loopback host:port (port 0: any). A daemon
// restart asks for its previous port first, so an agent that outlives it
// (a paseo session) still finds the proxy where its environment says.
func (s *Server) Listen(addr string) (string, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("pkgproxy: %s is not a loopback address", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 30 * time.Second}
	s.mu.Lock()
	s.ln, s.srv = ln, srv
	s.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), nil
}

// Addr is the listening address ("" before Listen).
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Close stops the server.
func (s *Server) Close() {
	s.mu.Lock()
	srv := s.srv
	s.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

// Token is c's credential: the caller, signed. It authenticates nothing but
// "this request may use the proxy, on behalf of c" — the proxy serves only
// public, verified content — and lets every fetch be attributed.
func (s *Server) Token(c Requester) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(c.Identity + "\x00" + c.Dispatch))
	return payload + "." + s.sign(payload)
}

func (s *Server) sign(payload string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("pkgproxy\x00" + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:20])
}

// caller verifies a token.
func (s *Server) caller(tok string) (Requester, bool) {
	payload, sig, ok := strings.Cut(tok, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.sign(payload))) {
		return Requester{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return Requester{}, false
	}
	id, disp, _ := strings.Cut(string(raw), "\x00")
	return Requester{Identity: id, Dispatch: disp}, true
}

// AgentEnv is the environment pointing every ecosystem's tool at this
// server on behalf of c; nil before Listen.
func (s *Server) AgentEnv(c Requester) []string {
	addr := s.Addr()
	if addr == "" {
		return nil
	}
	tok := s.Token(c)
	var env []string
	for _, n := range s.ord {
		env = append(env, s.ecos[n].AgentEnv("http://"+addr+"/"+tok+"/"+n)...)
	}
	return env
}

// ServeHTTP routes /<token>/<ecosystem>/<rest>.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "conductor package proxy: GET only", http.StatusMethodNotAllowed)
		return
	}
	p := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	tok, p, _ := strings.Cut(p, "/")
	c, ok := s.caller(tok)
	if !ok {
		http.Error(w, "conductor package proxy: no valid token in the URL (a sandboxed agent's environment carries one)", http.StatusForbidden)
		return
	}
	name, rest, _ := strings.Cut(p, "/")
	e, ok := s.ecos[name]
	if !ok {
		http.Error(w, fmt.Sprintf("conductor package proxy: no ecosystem %q here", name), http.StatusNotFound)
		return
	}
	emit := func(ev Event) {
		ev.Requester, ev.Ecosystem = c, name
		if s.OnEvent != nil {
			s.OnEvent(ev)
		}
	}
	e.Serve(w, r, rest, c, emit)
}

// LoadKey reads the token key at path, creating it (0600) on first use: a
// key that survives a restart keeps a running agent's token valid.
func LoadKey(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}
