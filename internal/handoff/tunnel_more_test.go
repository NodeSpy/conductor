package handoff

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// stubTool writes an executable named name into a dir prepended to PATH.
func stubTool(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/usr/bin/env bash\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestCommandTunnelOpen(t *testing.T) {
	dir := stubTool(t, "fakecf", `echo "args: $@" > "$(dirname "$0")/argv"
echo "ready at https://demo.trycloudflare.com/x"
sleep 5`)
	tun := commandTunnel{
		argv:    func(port string) []string { return []string{"fakecf", "--url", "http://localhost:" + port} },
		urlRe:   cloudflareURLRe,
		timeout: 5 * time.Second,
		log:     t.Logf,
	}
	url, closeFn, err := tun.Open(context.Background(), "127.0.0.1:8099")
	if err != nil || url != "https://demo.trycloudflare.com/x" {
		t.Fatalf("open: %q %v", url, err)
	}
	closeFn()
	argv, _ := os.ReadFile(filepath.Join(dir, "argv"))
	if !strings.Contains(string(argv), "--url http://localhost:8099") {
		t.Fatalf("argv: %s", argv)
	}
	// Bad listen address.
	if _, _, err := tun.Open(context.Background(), "nope"); err == nil {
		t.Fatal("bad addr should error")
	}
}

func TestTemplateTunnelExpansion(t *testing.T) {
	dir := stubTool(t, "mytun", `echo "args: $@" > "$(dirname "$0")/argv"
echo "https://tun.example/abc"
sleep 5`)
	tun := templateTunnel{
		command: []string{"mytun", "--port", "{{.port}}", "--addr", "{{.addr}}"},
		urlRe:   regexp.MustCompile(`https://\S+`),
		timeout: 5 * time.Second,
	}
	url, closeFn, err := tun.Open(context.Background(), ":8099")
	if err != nil || url != "https://tun.example/abc" {
		t.Fatalf("open: %q %v", url, err)
	}
	closeFn()
	argv, _ := os.ReadFile(filepath.Join(dir, "argv"))
	if !strings.Contains(string(argv), "--port 8099 --addr 127.0.0.1:8099") {
		t.Fatalf("template expansion: %s", argv)
	}
	if _, _, err := tun.Open(context.Background(), "bad-addr"); err == nil {
		t.Fatal("unparseable addr should error")
	}
}

func TestSSHTunnelArgvAndOpen(t *testing.T) {
	if got := strings.Join(sshArgv("pinggy", "80"), " "); !strings.Contains(got, "-p 443") || !strings.Contains(got, "0:localhost:80") {
		t.Fatalf("pinggy argv: %s", got)
	}
	if got := strings.Join(sshArgv("localhost.run", "80"), " "); !strings.Contains(got, "-R 80:localhost:80 localhost.run") {
		t.Fatalf("default argv: %s", got)
	}
	stubTool(t, "ssh", `echo "tunneled https://xyz.lhr.life"
sleep 5`)
	tun := sshTunnel{sshHost: "localhost.run", timeout: 5 * time.Second, log: t.Logf}
	url, closeFn, err := tun.Open(context.Background(), ":8099")
	if err != nil || url != "https://xyz.lhr.life" {
		t.Fatalf("ssh open: %q %v", url, err)
	}
	closeFn()
}

func TestTailscaleOpenAndStatusFallback(t *testing.T) {
	// serve prints the URL directly.
	stubTool(t, "tailscale", `case "$1" in
  serve|funnel) echo "Available at https://box.tailnet.ts.net/" ;;
  status) echo '{"Self":{"DNSName":"box.tailnet.ts.net."}}' ;;
esac`)
	tun := tailscaleTunnel{mode: "serve", timeout: 5 * time.Second, log: t.Logf}
	url, closeFn, err := tun.Open(context.Background(), ":8099")
	if err != nil || !strings.HasPrefix(url, "https://box.tailnet.ts.net") {
		t.Fatalf("serve open: %q %v", url, err)
	}
	closeFn()

	// No URL in the command output → the status fallback resolves DNSName.
	stubTool(t, "tailscale", `case "$1" in
  serve|funnel) echo "ok" ;;
  status) echo '{"Self":{"DNSName":"fb.tailnet.ts.net."}}' ;;
esac`)
	url, closeFn, err = tailscaleTunnel{mode: "funnel", timeout: 5 * time.Second, log: t.Logf}.Open(context.Background(), ":8099")
	if err != nil || url != "https://fb.tailnet.ts.net" {
		t.Fatalf("status fallback: %q %v", url, err)
	}
	closeFn()

	if _, _, err := (tailscaleTunnel{mode: "serve", timeout: time.Second}).Open(context.Background(), "bad"); err == nil {
		t.Fatal("bad addr should error")
	}
}

// TestNgrokOpenPollTimeout: with no local ngrok API, Open fails after the
// poll deadline instead of hanging.
func TestNgrokOpenPollTimeout(t *testing.T) {
	stubTool(t, "ngrok", `exec sleep 5`)
	tun := ngrokTunnel{authtoken: "tok", timeout: 400 * time.Millisecond, log: t.Logf}
	start := time.Now()
	_, _, err := tun.Open(context.Background(), ":8099")
	if err == nil || !strings.Contains(err.Error(), "no tunnel URL") {
		t.Fatalf("poll deadline: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("poll did not respect its deadline")
	}
	if _, _, err := tun.Open(context.Background(), "bad"); err == nil {
		t.Fatal("bad addr should error")
	}
}

func TestLanIPViaInterfaces(t *testing.T) {
	ip, err := lanIPViaInterfaces()
	if err != nil || ip == "" {
		t.Skip("no non-loopback interface on this host")
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.IsLoopback() {
		t.Fatalf("lan ip must be a non-loopback address, got %q", ip)
	}
}

func TestInboxUnregisterAndPresentationClose(t *testing.T) {
	inbox := NewInbox()
	inbox.register("C1", "t1")
	p := &slackPresentation{c: &SlackChannel{inbox: inbox}, channel: "C1", threadTS: "t1"}
	p.Close()
	if inbox.Deliver("C1", "t1", "approve") {
		t.Fatal("closed presentation must not receive deliveries")
	}
	inbox.register("D1", "")
	dp := &discordPresentation{c: &DiscordChannel{inbox: inbox}, channel: "D1"}
	dp.Close()
	if inbox.Deliver("D1", "", "approve") {
		t.Fatal("closed discord presentation must not receive deliveries")
	}
}

func TestAwaitCancelled(t *testing.T) {
	inbox := NewInbox()
	sp := &slackPresentation{c: &SlackChannel{inbox: inbox}, channel: "C9", threadTS: "t", pend: inbox.register("C9", "t")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sp.Await(ctx); err == nil {
		t.Fatal("cancelled slack await must error")
	}
	dp := &discordPresentation{c: &DiscordChannel{inbox: inbox}, channel: "D9", pend: inbox.register("D9", "")}
	if _, err := dp.Await(ctx); err == nil {
		t.Fatal("cancelled discord await must error")
	}
}
