// Package exposure holds the two vendor-neutral EXPOSURE builtins: connectors
// that make a local address reachable from outside, declared through the one
// contract's `exposes` verb semantic (docs/design/plugin-contract.md §2.3).
//
//   - lan: the address on this machine's LAN (a configured host, or the
//     detected private IPv4). No process.
//   - tunnel: runs any command that tunnels a local port, and reads the
//     public URL from its output — cloudflared, localxpose, an ssh -R to a
//     tunnel host, anything. One process per lease, killed on release.
//
// Named tunnel services (cloudflared, ngrok, tailscale, …) are plugins, not
// builtins: conductor names no tunnel vendor. Both builtins speak the
// contract in-process (pkg/plugin.ServeConn), so they take the path a spawned
// plugin takes.
package exposure

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/pkg/plugin"
)

// The exposes semantic both builtins declare: open {local_addr} →
// {public_url[, lease]}; close {lease}.
var exposes = &plugin.VerbSemantics{HostOnly: true, Exposes: &plugin.Exposes{Local: "local_addr", URL: "public_url"}}

// LAN is the `lan` exposure builtin.
type LAN struct{}

// Describe declares lan.
func (LAN) Describe() plugin.Decl {
	return plugin.Decl{
		Type: "lan", Kind: plugin.KindConnector,
		Desc: "Expose a local address on this machine's LAN (a host you name, or the detected private IPv4).",
		Connection: plugin.Schema{
			"host":   {Type: "string", Desc: "LAN host or IP to advertise (default: the detected private IPv4)"},
			"scheme": {Type: "string", Enum: []string{"http", "https"}, Desc: "URL scheme (default http)"},
		},
		Verbs: []plugin.Verb{{
			Name: "open", Desc: "the LAN URL for a local address", Semantics: exposes,
			Options: plugin.Schema{"local_addr": {Type: "string", Required: true}},
			Outputs: plugin.Schema{"public_url": {Type: "string", Required: true}},
		}},
	}
}

// Invoke serves open.
func (LAN) Invoke(req plugin.InvokeRequest) (plugin.InvokeResult, error) {
	if req.Verb != "open" {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, "lan has one verb, open", nil)
	}
	addr, _ := req.Options["local_addr"].(string)
	port, err := portOf(addr)
	if err != nil {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, "lan: "+err.Error(), nil)
	}
	host, _ := req.Connection["host"].(string)
	if host == "" {
		if host, err = detectLANIP(); err != nil {
			return plugin.InvokeResult{}, plugin.Fail(plugin.CodeUpstream, "lan: detect LAN IP: "+err.Error(), nil)
		}
	}
	scheme, _ := req.Connection["scheme"].(string)
	if scheme == "" {
		scheme = "http"
	}
	return plugin.InvokeResult{Outputs: map[string]any{"public_url": fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(host, port))}}, nil
}

// Tunnel is the `tunnel` exposure builtin.
type Tunnel struct {
	mu     sync.Mutex
	leases map[string]*lease // lease id → its process
	// Start is the URL wait bound (a var for tests).
	Start time.Duration
}

type lease struct {
	instance string
	stop     func()
}

// NewTunnel is the tunnel builtin.
func NewTunnel() *Tunnel { return &Tunnel{leases: map[string]*lease{}, Start: 30 * time.Second} }

// Describe declares tunnel.
func (t *Tunnel) Describe() plugin.Decl {
	sem := *exposes
	x := *sem.Exposes
	x.Lease, x.Release = "lease", "close"
	sem.Exposes = &x
	return plugin.Decl{
		Type: "tunnel", Kind: plugin.KindConnector,
		Desc: "Expose a local address through any tunnelling command; the public URL is read from its output.",
		Connection: plugin.Schema{
			"command":     {Type: "list", Required: true, Desc: "argv; {{.port}} and {{.addr}} expand to the local port and host:port"},
			"url_pattern": {Type: "string", Desc: "regexp matching the public URL in the command's output (default: the first http(s) URL)"},
		},
		Verbs: []plugin.Verb{
			{
				Name: "open", Desc: "start a tunnel to a local address", Semantics: &sem,
				Options: plugin.Schema{"local_addr": {Type: "string", Required: true}},
				Outputs: plugin.Schema{"public_url": {Type: "string", Required: true}, "lease": {Type: "string", Required: true}},
			},
			{
				Name: "close", Desc: "end a tunnel", Semantics: &plugin.VerbSemantics{HostOnly: true},
				Options: plugin.Schema{"lease": {Type: "string", Required: true}},
			},
		},
		Capabilities: plugin.Capabilities{Spawns: true},
	}
}

var defaultURL = regexp.MustCompile(`https?://\S+`)

// Invoke serves open and close.
func (t *Tunnel) Invoke(req plugin.InvokeRequest) (plugin.InvokeResult, error) {
	switch req.Verb {
	case "open":
		return t.open(req)
	case "close":
		id, _ := req.Options["lease"].(string)
		t.release(id)
		return plugin.InvokeResult{}, nil
	}
	return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, "tunnel verbs are open and close", nil)
}

func (t *Tunnel) open(req plugin.InvokeRequest) (plugin.InvokeResult, error) {
	addr, _ := req.Options["local_addr"].(string)
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, fmt.Sprintf("tunnel: local_addr %q is not host:port", addr), nil)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	raw, _ := req.Connection["command"].([]any)
	argv := make([]string, 0, len(raw))
	r := strings.NewReplacer("{{.port}}", port, "{{.addr}}", net.JoinHostPort(host, port))
	for _, a := range raw {
		s, ok := a.(string)
		if !ok {
			return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, "tunnel: command must be a list of strings", nil)
		}
		argv = append(argv, r.Replace(s))
	}
	if len(argv) == 0 {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, "tunnel: command is required", nil)
	}
	re := defaultURL
	if p, _ := req.Connection["url_pattern"].(string); p != "" {
		if re, err = regexp.Compile(p); err != nil {
			return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, "tunnel: url_pattern: "+err.Error(), nil)
		}
	}
	url, stop, err := run(argv, re, t.Start)
	if err != nil {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeUpstream, "tunnel: "+err.Error(), nil)
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	t.mu.Lock()
	t.leases[id] = &lease{instance: req.Instance, stop: stop}
	t.mu.Unlock()
	return plugin.InvokeResult{Outputs: map[string]any{"public_url": url, "lease": id}}, nil
}

func (t *Tunnel) release(id string) {
	t.mu.Lock()
	l := t.leases[id]
	delete(t.leases, id)
	t.mu.Unlock()
	if l != nil {
		l.stop()
	}
}

// Stop releases every lease the instance holds (plugin.stop).
func (t *Tunnel) Stop(_ context.Context, req plugin.StopRequest) error {
	t.mu.Lock()
	var ids []string
	for id, l := range t.leases {
		if l.instance == req.Instance {
			ids = append(ids, id)
		}
	}
	t.mu.Unlock()
	for _, id := range ids {
		t.release(id)
	}
	return nil
}

// run starts argv, scans its stdout and stderr for the first URL, and
// returns it with a stop that kills the process. The process outlives this
// call (it IS the tunnel) until stop.
func run(argv []string, re *regexp.Regexp, timeout time.Duration) (string, func(), error) {
	if _, err := exec.LookPath(argv[0]); err != nil {
		return "", nil, fmt.Errorf("%s not found on PATH: %w", argv[0], err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return "", nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return "", nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return "", nil, fmt.Errorf("start %s: %w", argv[0], err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			_ = cmd.Wait()
		})
	}
	found := make(chan string, 1)
	scan := func(r io.Reader) {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if m := re.FindString(sc.Text()); m != "" {
				select {
				case found <- m:
				default:
				}
			}
		}
	}
	go scan(stdout)
	go scan(stderr)
	select {
	case url := <-found:
		return url, stop, nil
	case <-time.After(timeout):
		stop()
		return "", nil, fmt.Errorf("%s: no URL in its output within %s", argv[0], timeout)
	}
}

func portOf(addr string) (string, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "", fmt.Errorf("local_addr %q is not host:port", addr)
	}
	return port, nil
}

// detectLANIP finds a private, non-loopback IPv4 for this machine: the
// source address the kernel would route a public destination from (UDP: no
// packet is sent), else the first private interface address.
func detectLANIP() (string, error) {
	if c, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		defer c.Close()
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && isPrivateIPv4(a.IP) {
			return a.IP.String(), nil
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && isPrivateIPv4(n.IP) {
			return n.IP.To4().String(), nil
		}
	}
	return "", fmt.Errorf("no private LAN IPv4 address found")
}

func isPrivateIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	return ip4[0] == 10 || (ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31) || (ip4[0] == 192 && ip4[1] == 168)
}

// Leases reports how many exposures are open (for tests and status).
func (t *Tunnel) Leases() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.leases)
}
