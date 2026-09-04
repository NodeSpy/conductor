package handoff

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// tunnelStartTimeout bounds how long a spawning provider gets to print/report its
// public URL before Open gives up and kills the process. Cloudflared/ngrok/ssh/etc
// typically report within a few seconds; 30s is generous headroom for a slow
// network or first-run binary download prompt (which would otherwise hang forever).
const tunnelStartTimeout = 30 * time.Second

// genericURLRe is the default URL-extraction pattern for the `command` provider
// and any preset that doesn't need a tighter one.
var genericURLRe = regexp.MustCompile(`https?://\S+`)

// Tunnel exposes conductor's local hand-off listener at a public (or LAN) URL.
// Open is called fresh per hand-off draft (see WebChannel.Present) so a
// spawning provider gets a new process and therefore a new URL each time;
// lan/static return a stable origin. closeFn tears down whatever Open started
// (kills the subprocess) and is always safe to call, including more than once.
type Tunnel interface {
	Open(ctx context.Context, localAddr string) (publicURL string, closeFn func() error, err error)
}

// NewTunnel builds the Tunnel for a HandoffWeb's tunnel config. baseURL is the
// entry's configured base_url, used verbatim by the ""/static provider (so an
// existing config with only base_url set keeps working unchanged). log may be
// nil.
func NewTunnel(cfg config.TunnelConfig, baseURL string, log func(string, ...any)) (Tunnel, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	switch cfg.Provider {
	case "", "static":
		return staticTunnel{baseURL: baseURL}, nil
	case "lan":
		return lanTunnel{host: cfg.Host}, nil
	case "cloudflared":
		return commandTunnel{
			argv: func(port string) []string {
				return []string{"cloudflared", "tunnel", "--url", "http://127.0.0.1:" + port}
			},
			urlRe:   cloudflareURLRe,
			timeout: tunnelStartTimeout,
			log:     log,
		}, nil
	case "ngrok":
		return ngrokTunnel{authtoken: cfg.Authtoken, timeout: tunnelStartTimeout, log: log}, nil
	case "tailscale":
		mode := cfg.Mode
		if mode == "" {
			mode = "serve"
		}
		if mode != "serve" && mode != "funnel" {
			return nil, fmt.Errorf("handoff: tunnel: tailscale mode must be serve|funnel, got %q", mode)
		}
		return tailscaleTunnel{mode: mode, timeout: tunnelStartTimeout, log: log}, nil
	case "ssh":
		if cfg.SSHHost == "" {
			return nil, fmt.Errorf("handoff: tunnel: ssh provider requires ssh_host (e.g. localhost.run, serveo.net, a.pinggy.io)")
		}
		return sshTunnel{sshHost: cfg.SSHHost, timeout: tunnelStartTimeout, log: log}, nil
	case "localxpose":
		return commandTunnel{
			argv:    func(port string) []string { return []string{"loclx", "tunnel", "http", "--to", "localhost:" + port} },
			urlRe:   loclxURLRe,
			timeout: tunnelStartTimeout,
			log:     log,
		}, nil
	case "command":
		if len(cfg.Command) == 0 {
			return nil, fmt.Errorf("handoff: tunnel: command provider requires a non-empty command:")
		}
		urlRe := genericURLRe
		if cfg.URLPattern != "" {
			re, err := regexp.Compile(cfg.URLPattern)
			if err != nil {
				return nil, fmt.Errorf("handoff: tunnel: invalid url_pattern %q: %w", cfg.URLPattern, err)
			}
			urlRe = re
		}
		return templateTunnel{command: cfg.Command, urlRe: urlRe, timeout: tunnelStartTimeout, log: log}, nil
	default:
		return nil, fmt.Errorf("handoff: tunnel: unknown provider %q", cfg.Provider)
	}
}

func noopClose() error { return nil }

// portOf pulls the port out of a "host:port" (or ":port") listen address — every
// spawning provider forwards to 127.0.0.1:<port>, or the tool's own CLI just
// wants the bare port.
func portOf(localAddr string) (string, error) {
	_, port, err := net.SplitHostPort(localAddr)
	if err != nil {
		return "", fmt.Errorf("parse listen addr %q: %w", localAddr, err)
	}
	if port == "" {
		return "", fmt.Errorf("listen addr %q has no port", localAddr)
	}
	return port, nil
}

// ---- static / lan (no subprocess) ---------------------------------------------

// staticTunnel is the ""/static provider: no process, the origin is always the
// configured base_url — today's (pre-tunnel) behavior, verbatim.
type staticTunnel struct{ baseURL string }

func (t staticTunnel) Open(context.Context, string) (string, func() error, error) {
	return t.baseURL, noopClose, nil
}

// lanTunnel exposes the listener at a LAN address: host is either the
// configured one or an auto-detected private IPv4, port comes from localAddr.
type lanTunnel struct{ host string }

func (t lanTunnel) Open(_ context.Context, localAddr string) (string, func() error, error) {
	port, err := portOf(localAddr)
	if err != nil {
		return "", nil, fmt.Errorf("handoff: tunnel: lan: %w", err)
	}
	host := t.host
	if host == "" {
		host, err = detectLANIP()
		if err != nil {
			return "", nil, fmt.Errorf("handoff: tunnel: lan: detect LAN IP: %w", err)
		}
	}
	return fmt.Sprintf("http://%s:%s", host, port), noopClose, nil
}

// detectLANIP finds a private, non-loopback IPv4 address for this machine.
// First choice: dial a UDP socket to a public address (no packets are actually
// sent for UDP — this just makes the kernel pick a source address/route) and
// read back the local address. Falls back to scanning network interfaces for a
// private IPv4 if the dial trick doesn't yield one (e.g. no default route).
func detectLANIP() (string, error) {
	if ip, err := lanIPViaDial(); err == nil {
		return ip, nil
	}
	return lanIPViaInterfaces()
}

func lanIPViaDial() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", fmt.Errorf("unexpected local addr type %T", conn.LocalAddr())
	}
	if !isPrivateIPv4(addr.IP) {
		return "", fmt.Errorf("dial-detected address %s is not a private LAN IPv4", addr.IP)
	}
	return addr.IP.String(), nil
}

func lanIPViaInterfaces() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil || !isPrivateIPv4(ip4) {
			continue
		}
		return ip4.String(), nil
	}
	return "", fmt.Errorf("no private LAN IPv4 address found on any interface")
}

// isPrivateIPv4 reports whether ip is in one of the RFC1918 private ranges.
func isPrivateIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	switch {
	case ip4[0] == 10:
		return true
	case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
		return true
	case ip4[0] == 192 && ip4[1] == 168:
		return true
	default:
		return false
	}
}

// ---- generic subprocess runner --------------------------------------------

// runTunnel starts argv, scans its combined stdout+stderr line by line for the
// first urlRe match, and returns that as publicURL together with a closeFn that
// kills the process. It errors if the binary isn't on PATH (named clearly), or
// if no URL is seen within timeout. The process is started against its own
// background context (not ctx) so it keeps running after Open returns, until
// closeFn is called; ctx is only used to bound (and, if cancelled, abort) the
// wait for the URL to appear.
func runTunnel(ctx context.Context, argv []string, urlRe *regexp.Regexp, timeout time.Duration, log func(string, ...any)) (string, func() error, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	if len(argv) == 0 {
		return "", nil, fmt.Errorf("empty command")
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return "", nil, fmt.Errorf("%s not found on PATH (install it): %w", argv[0], err)
	}

	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, argv[0], argv[1:]...)
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

	var closeOnce sync.Once
	closeFn := func() error {
		closeOnce.Do(func() {
			cancel()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
		})
		return nil
	}

	found := make(chan string, 1)
	scan := func(r io.Reader) {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := sc.Text()
			log("tunnel %s: %s", argv[0], line)
			if m := urlRe.FindString(line); m != "" {
				select {
				case found <- m:
				default:
				}
			}
		}
	}
	go scan(stdout)
	go scan(stderr)

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case url := <-found:
		return url, closeFn, nil
	case <-timer.C:
		closeFn()
		return "", nil, fmt.Errorf("%s: no URL detected within %s", argv[0], timeout)
	case <-ctx.Done():
		closeFn()
		return "", nil, ctx.Err()
	}
}

// commandTunnel runs a fixed argv template (a provider preset) and scans its
// output with a fixed urlRe. cloudflared/localxpose (and any future preset with
// no special protocol beyond "run it, scan stdout") are built on this.
type commandTunnel struct {
	argv    func(port string) []string
	urlRe   *regexp.Regexp
	timeout time.Duration
	log     func(string, ...any)
}

func (t commandTunnel) Open(ctx context.Context, localAddr string) (string, func() error, error) {
	port, err := portOf(localAddr)
	if err != nil {
		return "", nil, fmt.Errorf("handoff: tunnel: %w", err)
	}
	url, closeFn, err := runTunnel(ctx, t.argv(port), t.urlRe, t.timeout, t.log)
	if err != nil {
		return "", nil, fmt.Errorf("handoff: tunnel: %w", err)
	}
	return url, closeFn, nil
}

var cloudflareURLRe = regexp.MustCompile(`https://[a-zA-Z0-9.-]+\.trycloudflare\.com\S*`)

var loclxURLRe = regexp.MustCompile(`https://[a-zA-Z0-9.-]+\.loclx\.io\S*`)

// ---- ssh (+ presets: localhost.run, serveo.net, pinggy) -----------------------

// sshTunnel opens a remote port-forward tunnel over ssh. Most services (e.g.
// localhost.run, serveo.net) use the plain -R 80:localhost:<port> form; pinggy
// needs its own port/format. closeFn kills the ssh process, tearing the forward
// down.
type sshTunnel struct {
	sshHost string
	timeout time.Duration
	log     func(string, ...any)
}

func (t sshTunnel) Open(ctx context.Context, localAddr string) (string, func() error, error) {
	port, err := portOf(localAddr)
	if err != nil {
		return "", nil, fmt.Errorf("handoff: tunnel: ssh: %w", err)
	}
	url, closeFn, err := runTunnel(ctx, sshArgv(t.sshHost, port), genericURLRe, t.timeout, t.log)
	if err != nil {
		return "", nil, fmt.Errorf("handoff: tunnel: ssh: %w", err)
	}
	return url, closeFn, nil
}

// sshArgv builds the ssh invocation for sshHost. pinggy needs its own
// port/remote-forward form (-p 443, -R0:...); localhost.run/serveo.net (and any
// other host offering the same convention) use the common -R 80:localhost:<port>
// form.
func sshArgv(sshHost, port string) []string {
	switch sshHost {
	case "pinggy", "a.pinggy.io":
		return []string{"ssh", "-p", "443", "-o", "StrictHostKeyChecking=accept-new", "-R", "0:localhost:" + port, "a.pinggy.io"}
	default:
		return []string{"ssh", "-o", "StrictHostKeyChecking=accept-new", "-R", "80:localhost:" + port, sshHost}
	}
}

// ---- generic `command:` escape hatch -------------------------------------------

// templateTunnel runs a user-supplied command (with {{.port}}/{{.addr}} expanded)
// and scans its output with urlRe (default genericURLRe, or the configured
// url_pattern). Every named preset above is a canned instance of this shape.
type templateTunnel struct {
	command []string
	urlRe   *regexp.Regexp
	timeout time.Duration
	log     func(string, ...any)
}

func (t templateTunnel) Open(ctx context.Context, localAddr string) (string, func() error, error) {
	host, port, err := net.SplitHostPort(localAddr)
	if err != nil {
		return "", nil, fmt.Errorf("handoff: tunnel: command: parse listen addr %q: %w", localAddr, err)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	addr := host + ":" + port
	argv := make([]string, len(t.command))
	for i, a := range t.command {
		argv[i] = expandTunnelTemplate(a, port, addr)
	}
	url, closeFn, err := runTunnel(ctx, argv, t.urlRe, t.timeout, t.log)
	if err != nil {
		return "", nil, fmt.Errorf("handoff: tunnel: command: %w", err)
	}
	return url, closeFn, nil
}

// expandTunnelTemplate substitutes {{.port}} and {{.addr}} in one command:
// argument.
func expandTunnelTemplate(s, port, addr string) string {
	r := strings.NewReplacer("{{.port}}", port, "{{.addr}}", addr)
	return r.Replace(s)
}

// ---- ngrok (local API, not stdout scanning) ------------------------------------

// ngrokTunnel runs `ngrok http <port>` and reads the assigned public URL off
// ngrok's own local API (http://127.0.0.1:4040/api/tunnels) rather than scanning
// its TUI output. authtoken, when set, is passed on the command line; otherwise
// ngrok relies on its own configured default.
type ngrokTunnel struct {
	authtoken string
	timeout   time.Duration
	log       func(string, ...any)
}

func (t ngrokTunnel) Open(ctx context.Context, localAddr string) (string, func() error, error) {
	if t.log == nil {
		t.log = func(string, ...any) {}
	}
	port, err := portOf(localAddr)
	if err != nil {
		return "", nil, fmt.Errorf("handoff: tunnel: ngrok: %w", err)
	}
	if _, err := exec.LookPath("ngrok"); err != nil {
		return "", nil, fmt.Errorf("handoff: tunnel: ngrok: ngrok not found on PATH (install it): %w", err)
	}
	argv := []string{"ngrok", "http"}
	if t.authtoken != "" {
		argv = append(argv, "--authtoken", t.authtoken)
	}
	argv = append(argv, port)

	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, argv[0], argv[1:]...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		cancel()
		return "", nil, fmt.Errorf("handoff: tunnel: ngrok: start: %w", err)
	}
	var closeOnce sync.Once
	closeFn := func() error {
		closeOnce.Do(func() {
			cancel()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
		})
		return nil
	}

	url, err := pollNgrokAPI(ctx, t.timeout)
	if err != nil {
		// Kill + wait FIRST so the process stops writing buf before we read it.
		closeFn()
		t.log("tunnel ngrok: %v (output so far: %s)", err, strings.TrimSpace(buf.String()))
		return "", nil, fmt.Errorf("handoff: tunnel: ngrok: %w", err)
	}
	return url, closeFn, nil
}

// pollNgrokAPI retries the ngrok local API until it reports a tunnel or timeout
// elapses — the API isn't up the instant the process starts.
func pollNgrokAPI(ctx context.Context, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		if url, err := fetchNgrokTunnelURL("http://127.0.0.1:4040/api/tunnels"); err == nil && url != "" {
			return url, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("no tunnel URL from local API within %s", timeout)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func fetchNgrokTunnelURL(apiURL string) (string, error) {
	resp, err := http.Get(apiURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return parseNgrokTunnelsResponse(body)
}

// parseNgrokTunnelsResponse extracts the first tunnel's public_url from ngrok's
// `GET /api/tunnels` JSON body. Split out from fetchNgrokTunnelURL so the parser
// is testable against captured sample bodies without a real ngrok process.
func parseNgrokTunnelsResponse(body []byte) (string, error) {
	var out struct {
		Tunnels []struct {
			PublicURL string `json:"public_url"`
		} `json:"tunnels"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode ngrok tunnels response: %w", err)
	}
	for _, tun := range out.Tunnels {
		if tun.PublicURL != "" {
			return tun.PublicURL, nil
		}
	}
	return "", fmt.Errorf("no tunnels reported yet")
}

// ---- tailscale (serve: tailnet-private, funnel: public; headscale transparent) --

// tailscaleTunnel brings the local port up via `tailscale serve` (tailnet-only,
// the default) or `tailscale funnel` (public), then derives the resulting URL —
// works transparently against a headscale control server too, since that's
// purely a client-side `tailscale up --login-server=...` concern with no
// conductor-side code.
type tailscaleTunnel struct {
	mode    string // serve | funnel
	timeout time.Duration
	log     func(string, ...any)
}

var tailscaleURLRe = regexp.MustCompile(`https://\S+`)

func (t tailscaleTunnel) Open(ctx context.Context, localAddr string) (string, func() error, error) {
	port, err := portOf(localAddr)
	if err != nil {
		return "", nil, fmt.Errorf("handoff: tunnel: tailscale: %w", err)
	}
	if _, err := exec.LookPath("tailscale"); err != nil {
		return "", nil, fmt.Errorf("handoff: tunnel: tailscale: tailscale not found on PATH (install it): %w", err)
	}
	out, err := runOnce(ctx, []string{"tailscale", t.mode, "--bg", port}, t.timeout)
	if err != nil {
		return "", nil, fmt.Errorf("handoff: tunnel: tailscale %s: %w (%s)", t.mode, err, strings.TrimSpace(out))
	}
	url := tailscaleURLRe.FindString(out)
	if url == "" {
		url, err = tailscaleStatusURL(ctx, t.timeout)
		if err != nil {
			return "", nil, fmt.Errorf("handoff: tunnel: tailscale: no URL from %s output or status: %w", t.mode, err)
		}
	}
	mode := t.mode
	closeFn := func() error {
		_, _ = runOnce(context.Background(), []string{"tailscale", mode, "--https=443", "off"}, 10*time.Second)
		return nil
	}
	return url, closeFn, nil
}

// runOnce runs argv to completion (bounded by timeout) and returns its combined
// output — for tailscale's own short-lived control commands (bring the serve/
// funnel mapping up, query status, tear it down), as opposed to runTunnel's
// long-lived "stays running until Close" subprocess.
func runOnce(ctx context.Context, argv []string, timeout time.Duration) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// tailscaleStatusURL falls back to `tailscale status --json` (the Self.DNSName,
// which is stable and always resolvable on the tailnet/funnel) when the
// serve/funnel --bg command's own output didn't carry a URL.
func tailscaleStatusURL(ctx context.Context, timeout time.Duration) (string, error) {
	out, err := runOnce(ctx, []string{"tailscale", "status", "--json"}, timeout)
	if err != nil {
		return "", err
	}
	return parseTailscaleDNSName([]byte(out))
}

// parseTailscaleDNSName extracts https://<Self.DNSName> from `tailscale status
// --json` output. Split out so it's testable against a captured sample body.
func parseTailscaleDNSName(body []byte) (string, error) {
	var st struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return "", fmt.Errorf("decode tailscale status: %w", err)
	}
	name := strings.TrimSuffix(st.Self.DNSName, ".")
	if name == "" {
		return "", fmt.Errorf("no Self.DNSName in tailscale status")
	}
	return "https://" + name, nil
}
