// Command acme-jailprobe is a test connector plugin that tries, on request,
// each thing a plugin's OS jail must refuse — and each thing it must allow —
// and reports what happened. The daemon's jail tests launch it under the
// real jail and read the report; it is never shipped.
//
// Every check is one option naming its target; each result is "ok" or
// "err: <why>". stdout is the RPC transport; all logging goes to stderr.
package main

import (
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

	plugin "github.com/NodeSpy/conductor/pkg/plugin"
)

func describe() plugin.Decl {
	str := func(d string) plugin.Field { return plugin.Field{Type: "string", Desc: d} }
	return plugin.Decl{
		Type: "acme-jailprobe",
		Desc: "test plugin: probes its own jail",
		Verbs: []plugin.Verb{{
			Name: "probe",
			Desc: "try each named access and report the outcome",
			Options: plugin.Schema{
				"read":      str("files to read, one per line"),
				"write":     str("files to create, one per line"),
				"proxy_get": str("URLs to fetch through HTTP_PROXY, one per line"),
				"dial":      str("host:port addresses to dial directly, one per line"),
				"exec":      str("commands to run, one per line (a name or an absolute path)"),
				"listen":    str("an address to serve one line on"),
				"drop":      str("directories to copy a program into and run it from, one per line ($HOME and $TMPDIR are expanded)"),
				"run":       str("command lines to run, one per line, split on spaces ($HOME is expanded); the report has their output"),
			},
			Outputs: plugin.Schema{"report": {Type: "object", Required: true}},
		}},
		// The tests give the egress it may use in the Spec, per run (a
		// local server's port); the one command it may run is true.
		Capabilities: plugin.Capabilities{Commands: []string{"true"}},
	}
}

func lines(v any) []string {
	s, _ := v.(string)
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

func result(err error) string {
	if err != nil {
		return "err: " + err.Error()
	}
	return "ok"
}

func invoke(req plugin.InvokeRequest) (plugin.InvokeResult, error) {
	report := map[string]any{}
	for _, p := range lines(req.Options["read"]) {
		_, err := os.ReadFile(p)
		report["read "+p] = result(err)
	}
	for _, p := range lines(req.Options["write"]) {
		report["write "+p] = result(os.WriteFile(p, []byte("x"), 0o600))
	}
	for _, u := range lines(req.Options["proxy_get"]) {
		report["proxy_get "+u] = proxyGet(u)
	}
	for _, a := range lines(req.Options["dial"]) {
		c, err := net.DialTimeout("tcp", a, 3*time.Second)
		if err == nil {
			c.Close()
		}
		report["dial "+a] = result(err)
	}
	for _, c := range lines(req.Options["exec"]) {
		out, err := exec.Command(c).CombinedOutput()
		if err != nil && len(out) > 0 {
			err = fmt.Errorf("%w: %s", err, out)
		}
		report["exec "+c] = result(err)
	}
	for _, d := range lines(req.Options["drop"]) {
		report["drop "+d] = dropAndExec(os.ExpandEnv(d))
	}
	for _, l := range lines(req.Options["run"]) {
		report["run "+l] = run(os.ExpandEnv(l))
	}
	if a, _ := req.Options["listen"].(string); a != "" {
		report["listen "+a] = result(serve(a))
	}
	report["home"] = os.Getenv("HOME")
	report["home writable"] = result(os.WriteFile(filepath.Join(os.Getenv("HOME"), "probe"), []byte("x"), 0o600))
	return plugin.InvokeResult{Outputs: map[string]any{"report": report}}, nil
}

// dropAndExec copies a system program (true) into dir and runs the copy.
func dropAndExec(dir string) string {
	var src []byte
	for _, p := range []string{"/usr/bin/true", "/bin/true"} {
		if b, err := os.ReadFile(p); err == nil {
			src = b
			break
		}
	}
	if src == nil {
		return "err: no program to copy"
	}
	p := filepath.Join(dir, "dropped")
	if err := os.WriteFile(p, src, 0o755); err != nil {
		return "err: write: " + err.Error()
	}
	return result(exec.Command(p).Run())
}

// run runs a command line and reports its output.
func run(l string) string {
	f := strings.Fields(l)
	if len(f) == 0 {
		return "err: empty"
	}
	out, err := exec.Command(f[0], f[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("err: %v: %s", err, out)
	}
	return "ok " + strings.TrimSpace(string(out))
}

// proxyGet fetches u through HTTP_PROXY regardless of NO_PROXY: the point is
// to ask the egress proxy, whose answer is the policy.
func proxyGet(u string) string {
	pu, err := url.Parse(os.Getenv("HTTP_PROXY"))
	if err != nil || os.Getenv("HTTP_PROXY") == "" {
		return "err: no HTTP_PROXY"
	}
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := c.Get(u)
	if err != nil {
		return "err: " + err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	if resp.StatusCode != 200 {
		return fmt.Sprintf("err: status %d: %s", resp.StatusCode, b)
	}
	return "ok " + string(b)
}

// serve answers one line on a, in the background, for as long as the
// plugin runs.
func serve(a string) error {
	ln, err := net.Listen("tcp", a)
	if err != nil {
		return err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("JAILPROBE-OK\n"))
			c.Close()
		}
	}()
	return nil
}

func main() {
	if err := plugin.Serve(plugin.ConnectorFunc(describe, invoke)); err != nil {
		fmt.Fprintf(os.Stderr, "acme-jailprobe: serve: %v\n", err)
		os.Exit(1)
	}
}
