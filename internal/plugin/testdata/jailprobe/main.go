// Command jailprobe is run inside a plugin jail by the jail integration
// test. Each argument is one check; it prints "check<TAB>result" per check, then
// (with -listen) serves its declared address until stdin closes.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	var listen string
	for _, a := range os.Args[1:] {
		k, v, _ := strings.Cut(a, "=")
		if k == "listen" {
			listen = v
			continue
		}
		fmt.Printf("%s\t%s\n", a, check(k, v))
	}
	if listen != "" {
		ln, err := net.Listen("tcp", listen)
		if err != nil {
			fmt.Printf("listen=%v\n", err)
			return
		}
		go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "from-inside") }))
		fmt.Println("READY")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
}

func ok(err error) string {
	if err != nil {
		return "denied: " + err.Error()
	}
	return "ok"
}

func check(k, v string) string {
	switch k {
	case "read":
		_, err := os.ReadFile(v)
		return ok(err)
	case "exists":
		_, err := os.Lstat(v)
		return ok(err)
	case "write":
		return ok(os.WriteFile(v, []byte("x"), 0o644))
	case "writeexec":
		// Write a script and try to run it.
		if err := os.WriteFile(v, []byte("#!/bin/sh\necho ran\n"), 0o755); err != nil {
			return "denied: write: " + err.Error()
		}
		return ok(exec.Command(v).Run())
	case "copyexec":
		// Copy this program somewhere and run the copy.
		self, err := os.ReadFile("/proc/self/exe")
		if err != nil {
			exe, _ := os.Executable()
			self, _ = os.ReadFile(exe)
		}
		if err := os.WriteFile(v, self, 0o755); err != nil {
			return "denied: write: " + err.Error()
		}
		return ok(exec.Command(v, "env=PATH").Run())
	case "exec":
		// Did the program start? (Its own exit status does not matter.)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		prog, args, _ := strings.Cut(v, " ")
		var argv []string
		if args != "" {
			argv = strings.Fields(args)
		}
		err := exec.CommandContext(ctx, prog, argv...).Run()
		if _, exited := err.(*exec.ExitError); err == nil || exited {
			return "ok: ran"
		}
		return "denied: " + err.Error()
	case "out":
		// Run a command line (fields split on spaces) and report what it
		// printed: "ok:<stdout>" when it succeeded.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		f := strings.Fields(v)
		var stderr strings.Builder
		c := exec.CommandContext(ctx, f[0], f[1:]...)
		c.Stderr = &stderr
		out, err := c.Output()
		if err != nil {
			return "denied: " + err.Error() + " " + strings.TrimSpace(stderr.String())
		}
		return "ok:" + strings.ReplaceAll(strings.TrimSpace(string(out)), "\n", " | ")
	case "dialunix":
		c, err := net.DialTimeout("unix", v, 3*time.Second)
		if err == nil {
			c.Close()
		}
		return ok(err)
	case "run":
		out, err := exec.Command(v).CombinedOutput()
		if err != nil {
			return "denied: " + err.Error() + " " + strings.TrimSpace(string(out))
		}
		return "ok"
	case "env":
		if val, set := os.LookupEnv(v); set {
			return "set:" + val
		}
		return "unset"
	case "get":
		c := &http.Client{Timeout: 10 * time.Second}
		resp, err := c.Get(v)
		if err != nil {
			return "denied: " + err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			return fmt.Sprintf("denied: %d %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
		return "ok:" + strings.TrimSpace(string(b))
	case "dial":
		c, err := net.DialTimeout("tcp", v, 3*time.Second)
		if err == nil {
			c.Close()
		}
		return ok(err)
	case "ls":
		ents, err := os.ReadDir(v)
		if err != nil {
			return "denied: " + err.Error()
		}
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		return "ok:" + strings.Join(names, ",")
	case "mkdir":
		return ok(os.MkdirAll(filepath.Join(v, "x"), 0o755))
	}
	return "unknown check"
}
