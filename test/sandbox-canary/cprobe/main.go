// cprobe is the container canary's probe (test/sandbox-canary/container.sh):
// a static binary copied into an agent's workspace and run there by
// `conductor sandbox exec`, inside the agent's container. It prints one
// "ok …" or "FAIL …" line per check.
//
//	cprobe boundary --decoy PATH --other-ws PATH --allowed URL --denied URL
//	cprobe install NAME   — a tool in HOME's ~/.local/bin
//	cprobe run NAME       — runs it by name (PATH)
package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func say(ok bool, what string) {
	if ok {
		fmt.Println("ok   " + what)
	} else {
		fmt.Println("FAIL " + what)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cprobe boundary|install|run …")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "install":
		dir := filepath.Join(os.Getenv("HOME"), ".local", "bin")
		_ = os.MkdirAll(dir, 0o755)
		err := os.WriteFile(filepath.Join(dir, os.Args[2]), []byte("#!/bin/sh\necho \"tool "+os.Args[2]+" ran\"\n"), 0o755)
		say(err == nil, "installed "+os.Args[2]+" into HOME")
	case "run":
		out, err := exec.Command(os.Args[2]).CombinedOutput()
		say(err == nil && strings.TrimSpace(string(out)) == "tool "+os.Args[2]+" ran", "ran "+os.Args[2]+" from HOME: "+strings.TrimSpace(string(out)))
	case "boundary":
		boundary(os.Args[2:])
	}
}

func boundary(args []string) {
	fs := flag.NewFlagSet("boundary", flag.ExitOnError)
	decoy := fs.String("decoy", "", "")
	otherWS := fs.String("other-ws", "", "")
	allowed := fs.String("allowed", "", "")
	denied := fs.String("denied", "", "")
	_ = fs.Parse(args)

	host, _ := os.Hostname()
	say(host == "conductor-agent", "in an agent container (hostname "+host+")")
	say(os.Getenv("HOME") == "/home/agent", "HOME is the container's")
	say(os.Getuid() != 0, fmt.Sprintf("not root (uid %d)", os.Getuid()))
	status, _ := os.ReadFile("/proc/self/status")
	say(strings.Contains(string(status), "CapEff:\t0000000000000000"), "no capabilities")
	say(strings.Contains(string(status), "NoNewPrivs:\t1"), "no-new-privileges")
	say(strings.Contains(string(status), "Seccomp:\t2"), "seccomp filter")
	if *decoy != "" {
		_, err := os.ReadFile(*decoy)
		say(err != nil, "the operator's secret is unreadable")
	}
	if *otherWS != "" {
		_, err := os.Stat(*otherWS)
		say(err != nil, "another identity's workspace is absent")
	}
	for _, s := range []string{"/var/run/docker.sock", "/run/docker.sock", "/run/podman/podman.sock", "/var/run/podman/podman.sock", "/run/containerd/containerd.sock"} {
		_, err := os.Stat(s)
		say(err != nil, "no engine socket at "+s)
	}
	say(os.Getenv("DOCKER_HOST") == "" && os.Getenv("CONTAINER_HOST") == "", "no engine address in the environment")
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.Contains(k, "GH_") || strings.Contains(k, "GITHUB") || strings.HasPrefix(k, "AWS_") || k == "SSH_AUTH_SOCK" {
			say(false, "credential in the environment: "+k)
		}
	}
	_, err := net.DialTimeout("tcp", "1.1.1.1:443", 3*time.Second)
	say(err != nil, "no network of its own")
	pu, perr := url.Parse(os.Getenv("HTTPS_PROXY"))
	say(perr == nil && os.Getenv("HTTPS_PROXY") != "", "an allowlist proxy is configured")
	cl := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	if *allowed != "" {
		r, err := cl.Get(*allowed)
		code := 0
		if err == nil {
			_, _ = io.Copy(io.Discard, r.Body)
			r.Body.Close()
			code = r.StatusCode
		}
		say(err == nil && code < 400, fmt.Sprintf("allowlisted egress %s (%d %v)", *allowed, code, err))
	}
	if *denied != "" {
		_, err := cl.Get(*denied)
		say(err != nil && strings.Contains(err.Error(), "Forbidden"), fmt.Sprintf("egress off the allowlist refused (%v)", err))
	}
	ep := strings.TrimPrefix(os.Getenv("CONDUCTOR_ENDPOINT"), "unix://")
	if ep != "" {
		c, err := net.DialTimeout("unix", ep, 3*time.Second)
		if c != nil {
			c.Close()
		}
		say(err == nil, "the conductor socket is reachable ("+ep+")")
	}
}
