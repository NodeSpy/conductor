package handoff

import (
	"context"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// ---- URL extraction (fed captured sample output, no real binaries/network) ----

func TestCloudflareURLRegexExtractsFromSampleOutput(t *testing.T) {
	sample := `2026-09-02T12:00:00Z INF Requesting new quick Tunnel on trycloudflare.com...
2026-09-02T12:00:01Z INF +--------------------------------------------------------------------------------------------+
2026-09-02T12:00:01Z INF |  Your quick Tunnel has been created! Visit it at (it may take some time to be reachable):   |
2026-09-02T12:00:01Z INF |  https://random-words-here.trycloudflare.com                                               |
2026-09-02T12:00:01Z INF +--------------------------------------------------------------------------------------------+`
	got := cloudflareURLRe.FindString(sample)
	want := "https://random-words-here.trycloudflare.com"
	if got != want {
		t.Fatalf("cloudflareURLRe: got %q, want %q", got, want)
	}
}

func TestCloudflareURLRegexNoMatch(t *testing.T) {
	if got := cloudflareURLRe.FindString("still starting up, no url yet"); got != "" {
		t.Fatalf("expected no match, got %q", got)
	}
}

func TestLoclxURLRegexExtractsFromSampleOutput(t *testing.T) {
	sample := "INFO[0000] Tunnel started at https://abc123.loclx.io -> localhost:8099"
	got := loclxURLRe.FindString(sample)
	want := "https://abc123.loclx.io"
	if got != want {
		t.Fatalf("loclxURLRe: got %q, want %q", got, want)
	}
}

// TestGenericURLRegexPerLine mirrors runTunnel's own scan: it feeds one line of
// CAPTURED sample output at a time (not the whole multi-line banner at once) —
// the same shape localhost.run/serveo.net/pinggy print on ssh connect.
func TestGenericURLRegexPerLine(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"Connect to https://a1b2c3.localhost.run or use the address above", "https://a1b2c3.localhost.run"},
		{"tunneled with tls termination, https://a1b2c3.localhost.run", "https://a1b2c3.localhost.run"},
		{"Web Debugger: http://127.0.0.1:4300", "http://127.0.0.1:4300"},
		{"https://rndsub.a.pinggy.link", "https://rndsub.a.pinggy.link"},
		{"no url on this line", ""},
	}
	for _, tc := range cases {
		if got := genericURLRe.FindString(tc.line); got != tc.want {
			t.Errorf("genericURLRe.FindString(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestNgrokTunnelsResponseParsing(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{
			name: "one tunnel",
			body: `{"tunnels":[{"name":"command_line","uri":"/api/tunnels/command_line","public_url":"https://abcd1234.ngrok-free.app","proto":"https","config":{"addr":"http://localhost:8099","inspect":true}}],"uri":"/api/tunnels"}`,
			want: "https://abcd1234.ngrok-free.app",
		},
		{
			name: "http and https variants, first wins",
			body: `{"tunnels":[{"public_url":"http://abcd1234.ngrok-free.app"},{"public_url":"https://abcd1234.ngrok-free.app"}]}`,
			want: "http://abcd1234.ngrok-free.app",
		},
		{
			name:    "no tunnels yet",
			body:    `{"tunnels":[]}`,
			wantErr: true,
		},
		{
			name:    "not json",
			body:    `not json at all`,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseNgrokTunnelsResponse([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got url %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTailscaleDNSNameParsing(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{
			name: "typical status",
			body: `{"Self":{"DNSName":"my-machine.tailnet-1234.ts.net.","OS":"linux"}}`,
			want: "https://my-machine.tailnet-1234.ts.net",
		},
		{
			name:    "empty DNSName",
			body:    `{"Self":{"DNSName":""}}`,
			wantErr: true,
		},
		{
			name:    "not json",
			body:    `nope`,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTailscaleDNSName([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got url %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTailscaleURLRegexOnBgOutput(t *testing.T) {
	sample := "Available on the internet:\nhttps://my-machine.tailnet-1234.ts.net/\n"
	got := tailscaleURLRe.FindString(sample)
	want := "https://my-machine.tailnet-1234.ts.net/"
	if got != want {
		t.Fatalf("tailscaleURLRe: got %q, want %q", got, want)
	}
}

// ---- ssh argv presets -----------------------------------------------------

func TestSSHArgvPinggyPreset(t *testing.T) {
	for _, host := range []string{"pinggy", "a.pinggy.io"} {
		argv := sshArgv(host, "8099")
		joined := strings.Join(argv, " ")
		if !strings.Contains(joined, "-p 443") || !strings.Contains(joined, "-R 0:localhost:8099") || !strings.HasSuffix(joined, "a.pinggy.io") {
			t.Fatalf("pinggy argv for %q looks wrong: %v", host, argv)
		}
	}
}

func TestSSHArgvGenericHost(t *testing.T) {
	for _, host := range []string{"localhost.run", "serveo.net", "ssh.example.com"} {
		argv := sshArgv(host, "8099")
		joined := strings.Join(argv, " ")
		if !strings.Contains(joined, "-R 80:localhost:8099") || !strings.HasSuffix(joined, host) {
			t.Fatalf("generic argv for %q looks wrong: %v", host, argv)
		}
	}
}

// ---- LAN-IP detection -------------------------------------------------------

func TestIsPrivateIPv4(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.0.0.5", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"172.32.0.1", false},
		{"192.168.1.1", true},
		{"8.8.8.8", false},
		{"127.0.0.1", false}, // loopback is not treated as a LAN address by this predicate
		{"203.0.113.5", false},
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if got := isPrivateIPv4(ip); got != tc.want {
			t.Errorf("isPrivateIPv4(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

// TestDetectLANIPReturnsPrivateOrErrorsCleanly exercises the real detector: in a
// sandboxed/offline test environment it may legitimately fail (no default
// route, no non-loopback interface) — the assertion is just that it never
// returns a non-private/garbage address.
func TestDetectLANIPReturnsPrivateOrErrorsCleanly(t *testing.T) {
	ip, err := detectLANIP()
	if err != nil {
		t.Logf("detectLANIP: no LAN IP available in this environment: %v", err)
		return
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		t.Fatalf("detectLANIP returned an unparseable address: %q", ip)
	}
	if !isPrivateIPv4(parsed) {
		t.Fatalf("detectLANIP returned a non-private address: %q", ip)
	}
}

// ---- static / lan providers: stable origin, no-op close -----------------------

func TestStaticTunnelOpenIsNoopClose(t *testing.T) {
	tun, err := NewTunnel(config.TunnelConfig{Provider: "static"}, "https://conductor.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	url, closeFn, err := tun.Open(context.Background(), ":8099")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://conductor.example.com" {
		t.Fatalf("static tunnel should return base_url verbatim, got %q", url)
	}
	if err := closeFn(); err != nil {
		t.Fatalf("static tunnel close should be a no-op, got %v", err)
	}
}

func TestEmptyProviderIsStaticTunnel(t *testing.T) {
	tun, err := NewTunnel(config.TunnelConfig{}, "https://conductor.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	url, _, err := tun.Open(context.Background(), ":8099")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://conductor.example.com" {
		t.Fatalf("empty provider should behave like static, got %q", url)
	}
}

func TestLANTunnelOpenExplicitHost(t *testing.T) {
	tun, err := NewTunnel(config.TunnelConfig{Provider: "lan", Host: "192.168.1.42"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	url, closeFn, err := tun.Open(context.Background(), "0.0.0.0:8099")
	if err != nil {
		t.Fatal(err)
	}
	if url != "http://192.168.1.42:8099" {
		t.Fatalf("got %q", url)
	}
	if err := closeFn(); err != nil {
		t.Fatalf("lan tunnel close should be a no-op, got %v", err)
	}
}

func TestLANTunnelOpenBadListenAddr(t *testing.T) {
	tun, err := NewTunnel(config.TunnelConfig{Provider: "lan", Host: "192.168.1.42"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := tun.Open(context.Background(), "not-a-valid-addr"); err == nil {
		t.Fatal("an unparseable listen addr should error")
	}
}

// ---- unknown provider ----------------------------------------------------

func TestNewTunnelUnknownProviderErrors(t *testing.T) {
	if _, err := NewTunnel(config.TunnelConfig{Provider: "carrier-pigeon"}, "", nil); err == nil {
		t.Fatal("an unknown provider should error")
	}
}

func TestNewTunnelTailscaleBadModeErrors(t *testing.T) {
	if _, err := NewTunnel(config.TunnelConfig{Provider: "tailscale", Mode: "bogus"}, "", nil); err == nil {
		t.Fatal("an invalid tailscale mode should error")
	}
}

func TestNewTunnelSSHRequiresHost(t *testing.T) {
	if _, err := NewTunnel(config.TunnelConfig{Provider: "ssh"}, "", nil); err == nil {
		t.Fatal("ssh provider without ssh_host should error")
	}
}

func TestNewTunnelCommandRequiresCommand(t *testing.T) {
	if _, err := NewTunnel(config.TunnelConfig{Provider: "command"}, "", nil); err == nil {
		t.Fatal("command provider without command: should error")
	}
}

func TestNewTunnelCommandBadURLPatternErrors(t *testing.T) {
	if _, err := NewTunnel(config.TunnelConfig{Provider: "command", Command: []string{"echo"}, URLPattern: "(unclosed"}, "", nil); err == nil {
		t.Fatal("an invalid url_pattern should error")
	}
}

// ---- command provider end-to-end (a tiny stub command, no real tunnel tool) ---

func TestCommandProviderOpenScanClose(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	tun, err := NewTunnel(config.TunnelConfig{
		Provider: "command",
		Command:  []string{"sh", "-c", "echo https://x.example; sleep 5"},
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url, closeFn, err := tun.Open(ctx, "127.0.0.1:8099")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://x.example" {
		t.Fatalf("got %q", url)
	}
	if closeFn == nil {
		t.Fatal("expected a non-nil closeFn")
	}
	// Close must kill the still-sleeping process without hanging the test.
	done := make(chan error, 1)
	go func() { done <- closeFn() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("closeFn: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closeFn did not return — process not killed")
	}
	// Idempotent: calling it again must not hang or panic.
	if err := closeFn(); err != nil {
		t.Fatalf("second closeFn call: %v", err)
	}
}

func TestCommandProviderTemplateExpansion(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	tun, err := NewTunnel(config.TunnelConfig{
		Provider: "command",
		Command:  []string{"sh", "-c", "echo https://{{.port}}.example -- {{.addr}}; sleep 5"},
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url, closeFn, err := tun.Open(ctx, "127.0.0.1:9911")
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if url != "https://9911.example" {
		t.Fatalf("template port not expanded correctly, got %q", url)
	}
}

func TestCommandProviderCustomURLPattern(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	tun, err := NewTunnel(config.TunnelConfig{
		Provider:   "command",
		Command:    []string{"sh", "-c", "echo tunnel-ready id=abc123; sleep 5"},
		URLPattern: `id=\S+`,
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url, closeFn, err := tun.Open(ctx, "127.0.0.1:8099")
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if url != "id=abc123" {
		t.Fatalf("custom url_pattern not applied, got %q", url)
	}
}

func TestCommandProviderMissingBinaryErrorsClearly(t *testing.T) {
	tun, err := NewTunnel(config.TunnelConfig{
		Provider: "command",
		Command:  []string{"definitely-not-a-real-binary-xyz"},
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = tun.Open(context.Background(), "127.0.0.1:8099")
	if err == nil {
		t.Fatal("a missing binary should error")
	}
	if !strings.Contains(err.Error(), "definitely-not-a-real-binary-xyz") {
		t.Fatalf("error should name the missing binary, got: %v", err)
	}
}

func TestCommandProviderNoURLTimesOut(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	tun := templateTunnel{
		command: []string{"sh", "-c", "echo nothing-url-shaped-here; sleep 5"},
		urlRe:   genericURLRe,
		timeout: 200 * time.Millisecond,
		log:     nil,
	}
	start := time.Now()
	_, _, err := tun.Open(context.Background(), "127.0.0.1:8099")
	if err == nil {
		t.Fatal("expected a timeout error when no URL ever appears")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
}

// ---- portOf helper ----------------------------------------------------------

func TestPortOf(t *testing.T) {
	cases := []struct {
		addr    string
		want    string
		wantErr bool
	}{
		{":8099", "8099", false},
		{"127.0.0.1:8099", "8099", false},
		{"0.0.0.0:9100", "9100", false},
		{"not-an-addr", "", true},
	}
	for _, tc := range cases {
		got, err := portOf(tc.addr)
		if tc.wantErr {
			if err == nil {
				t.Errorf("portOf(%q): expected error", tc.addr)
			}
			continue
		}
		if err != nil {
			t.Errorf("portOf(%q): unexpected error %v", tc.addr, err)
			continue
		}
		if got != tc.want {
			t.Errorf("portOf(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

// ---- template expansion -----------------------------------------------------

func TestExpandTunnelTemplate(t *testing.T) {
	got := expandTunnelTemplate("--to localhost:{{.port}} at {{.addr}}", "8099", "127.0.0.1:8099")
	want := "--to localhost:8099 at 127.0.0.1:8099"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
