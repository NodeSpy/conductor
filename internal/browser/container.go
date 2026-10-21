package browser

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/browser/cdp"
)

// The browser in a container: Linux Chromium WITH its own sandbox, inside
// a container that has no network, no capability, a read-only root and no
// mount of the host. conductor reaches it over the engine's stdin/stdout
// alone (mux.go): the DevTools pipe, each proxy connection — piped into
// conductor's egress proxy with the browser's allowlist, as on Linux — and
// file transfers. Two layers: the container, and Chromium's own sandbox
// (its namespaces and seccomp-BPF) for every renderer. It is how macOS gets
// what Linux has natively, through Docker Desktop, colima or podman's VM.

// ContainerEngine is a container engine's CLI. It is the container
// backend's engine seam (agentsandbox.ResolveEngine on feat/agent-container)
// in small: when that lands, FindEngine becomes a call to it.
type ContainerEngine struct {
	Bin, Kind string
	// Env is the client's environment: what finds the engine, nothing else.
	Env []string
}

// FindEngine finds a container engine: pref (docker | podman | a path)
// when set, else Docker (Docker Desktop, colima) then podman on macOS,
// podman then docker elsewhere. It mirrors the container backend's
// agentsandbox.ResolveEngine (feat/agent-container) and becomes a call to
// it once both have landed.
func FindEngine(pref, goos string, lookPath func(string) (string, error), env []string) (*ContainerEngine, error) {
	order := []string{"podman", "docker"}
	if goos == "darwin" {
		order = []string{"docker", "podman"}
	}
	if pref != "" {
		order = []string{pref}
	}
	for _, name := range order {
		bin, err := lookPath(name)
		if err != nil {
			continue
		}
		kind := filepath.Base(name)
		if kind != "docker" && kind != "podman" {
			return nil, fmt.Errorf("browser: container engine %q is neither docker nor podman", name)
		}
		return &ContainerEngine{Bin: bin, Kind: kind, Env: engineEnv(env)}, nil
	}
	return nil, fmt.Errorf("browser: no container engine (%s) on PATH", strings.Join(order, ", "))
}

// engineEnv keeps what an engine CLI needs to find its engine.
func engineEnv(env []string) []string {
	keep := map[string]bool{"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "TMPDIR": true, "XDG_RUNTIME_DIR": true,
		"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "DBUS_SESSION_BUS_ADDRESS": true}
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if keep[k] || strings.HasPrefix(k, "DOCKER_") || strings.HasPrefix(k, "CONTAINER_") || strings.HasPrefix(k, "CONTAINERS_") || strings.HasPrefix(k, "PODMAN_") {
			out = append(out, kv)
		}
	}
	return out
}

// Ping reports whether the engine answers: Docker Desktop installed but
// not running is no engine.
func (e *ContainerEngine) Ping(ctx context.Context) error {
	if out, err := e.cmd(ctx, "version").CombinedOutput(); err != nil {
		return fmt.Errorf("browser: container engine %s is installed but not answering: %v: %s", e.Bin, err, clip(strings.TrimSpace(string(out)), 300))
	}
	return nil
}

func (e *ContainerEngine) cmd(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, e.Bin, args...)
	c.Env = e.Env
	return c
}

// Run runs one engine command and returns its output.
func (e *ContainerEngine) Run(ctx context.Context, args ...string) ([]byte, error) {
	c := e.cmd(ctx, args...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 600 {
			msg = msg[len(msg)-600:]
		}
		return out, fmt.Errorf("%s %s: %v: %s", e.Kind, args[0], err, msg)
	}
	return out, nil
}

//go:embed containershim/main.go
var shimSource []byte

//go:embed containershim/seccomp-default.json
var seccompDefault []byte

// imageDockerfile builds the browser image: the shim from its one source
// file, then Alpine's Chromium.
const imageDockerfile = `FROM golang:1.26-alpine AS shim
WORKDIR /src
COPY shim/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags=-s -o /cbshim .

FROM alpine:3.20
RUN apk add --no-cache chromium font-dejavu && adduser -D -u 1000 -h /home/browser browser
COPY --from=shim /cbshim /usr/local/bin/cbshim
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/cbshim"]
`

// ImageTag is the browser image's tag: its content's hash.
func ImageTag() string {
	h := sha256.New()
	h.Write([]byte(imageDockerfile))
	h.Write(shimSource)
	return "conductor-browser:" + hex.EncodeToString(h.Sum(nil))[:12]
}

var imageMu sync.Mutex

// EnsureImage builds the browser image unless the engine has it.
func (e *ContainerEngine) EnsureImage(ctx context.Context, logf func(string, ...any)) (string, error) {
	imageMu.Lock()
	defer imageMu.Unlock()
	tag := ImageTag()
	if _, err := e.Run(ctx, "image", "inspect", tag); err == nil {
		return tag, nil
	}
	if logf != nil {
		logf("browser: building the browser image %s with %s (once; Alpine's Chromium and conductor's shim)", tag, e.Kind)
	}
	var ctxTar bytes.Buffer
	tw := tar.NewWriter(&ctxTar)
	for name, body := range map[string][]byte{
		"Dockerfile":   []byte(imageDockerfile),
		"shim/main.go": shimSource,
		"shim/go.mod":  []byte("module cbshim\n\ngo 1.22\n"),
	} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), ModTime: time.Unix(0, 0)})
		_, _ = tw.Write(body)
	}
	_ = tw.Close()
	c := e.cmd(ctx, "build", "-t", tag, "-")
	c.Stdin = &ctxTar
	out, err := c.CombinedOutput()
	if err != nil {
		tail := string(out)
		if len(tail) > 1500 {
			tail = tail[len(tail)-1500:]
		}
		return "", fmt.Errorf("browser: build the browser image: %v\n%s", err, tail)
	}
	return tag, nil
}

// SeccompProfile is the engine's default seccomp profile with exactly what
// Chromium's own sandbox needs added: clone with namespace flags, unshare,
// setns and chroot (its namespace sandbox builds a user, pid and network
// namespace and chroots each renderer). clone3 stays ENOSYS (libc falls
// back to clone). Nothing else changes. (The default profile is moby's,
// Apache-2.0: containershim/seccomp-default.json.)
func SeccompProfile() ([]byte, error) {
	var p map[string]any
	if err := json.Unmarshal(seccompDefault, &p); err != nil {
		return nil, err
	}
	widen := map[string]bool{"clone": true, "clone3": true, "unshare": true, "setns": true, "chroot": true}
	var rules []any
	for _, r := range p["syscalls"].([]any) {
		rule := r.(map[string]any)
		var names []any
		for _, n := range rule["names"].([]any) {
			if !widen[n.(string)] {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			continue
		}
		rule["names"] = names
		rules = append(rules, rule)
	}
	rules = append(rules,
		map[string]any{"names": []string{"clone", "unshare", "setns", "chroot"}, "action": "SCMP_ACT_ALLOW"},
		map[string]any{"names": []string{"clone3"}, "action": "SCMP_ACT_ERRNO", "errnoRet": 38})
	p["syscalls"] = rules
	return json.Marshal(p)
}

// guestDir is the browser's directory inside the container (a tmpfs).
const guestDir = "/home/browser/run"

// containerArgs is the engine argv that runs the browser.
func containerArgs(name, image, seccomp string, flags []string) []string {
	args := []string{"run", "--rm", "-i", "--name", name, "--label", "conductor.browser=1",
		"--network", "none", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--security-opt", "seccomp=" + seccomp,
		"--read-only", "--tmpfs", "/home/browser:rw,exec,size=1g,mode=0700,uid=1000,gid=1000",
		"--tmpfs", "/tmp:rw,size=256m,mode=1777", "--shm-size", "256m",
		"--memory", "2g", "--pids-limit", "1024", "--user", "1000:1000",
		"--env", "HOME=/home/browser",
		image, "--listen", "127.0.0.1:18080", "--xfer", guestDir + "/xfer", "--", "chromium-browser"}
	return append(args, flags...)
}

// launchContainer starts the browser in a container.
func launchContainer(spec LaunchSpec) (*Process, error) {
	e := spec.Engine
	if spec.Image == "" {
		return nil, fmt.Errorf("browser: no browser image")
	}
	// The host side of transfers: uploads are staged here, downloads
	// fetched here, on their way through the stream.
	if err := os.MkdirAll(filepath.Join(spec.Dir, "xfer"), 0o700); err != nil {
		return nil, err
	}
	sock, closeProxy, err := spec.Proxy.TrustedUnixEndpoint(spec.Allow)
	if err != nil {
		return nil, err
	}
	prof, err := SeccompProfile()
	if err != nil {
		closeProxy()
		return nil, err
	}
	profPath := filepath.Join(spec.Dir, "seccomp.json")
	if err := os.WriteFile(profPath, prof, 0o600); err != nil {
		closeProxy()
		return nil, err
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "conductor-browser-" + hex.EncodeToString(b)
	flags := ChromeArgs(guestDir, "127.0.0.1:18080", false)
	c := exec.Command(e.Bin, containerArgs(name, spec.Image, profPath, flags)...)
	c.Env = e.Env
	stdin, err := c.StdinPipe()
	if err != nil {
		closeProxy()
		return nil, err
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		closeProxy()
		return nil, err
	}
	if spec.Stderr != nil {
		c.Stderr = spec.Stderr
	}
	setDeathSignal(c)
	if err := c.Start(); err != nil {
		closeProxy()
		return nil, fmt.Errorf("browser: %s run: %w", e.Kind, err)
	}
	mux := NewMux(stdout, stdin, func() (net.Conn, error) { return net.Dial("unix", sock) })
	w, r := mux.CDP()
	p := &Process{Conn: cdp.New(w, r), cmd: c, done: make(chan struct{}), Mux: mux, Guest: guestDir,
		kill: func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, _ = e.Run(ctx, "kill", name)
		}}
	p.stop = func() { closeProxy() }
	go func() {
		_ = c.Wait()
		p.stop()
		close(p.done)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := p.Conn.Call(ctx, "", "Browser.getVersion", nil, nil); err != nil {
		p.Close()
		msg := ""
		if spec.Stderr != nil {
			msg = strings.TrimSpace(spec.Stderr.String())
		}
		if len(msg) > 2000 {
			msg = msg[len(msg)-2000:]
		}
		return nil, fmt.Errorf("browser: the container browser did not start: %v\n%s", err, msg)
	}
	return p, nil
}

// SandboxStatus reads Chromium's own sandbox report (chrome://sandbox) in a
// fresh tab: what the container mode promises — and the self-test that
// gates it.
func SandboxStatus(ctx context.Context, p *Process) (string, error) {
	in, err := newInstance(ctx, p, "", nil)
	if err != nil {
		return "", err
	}
	t, err := in.current(ctx)
	if err != nil {
		return "", err
	}
	if err := in.conn().Call(ctx, t.session, "Page.navigate", map[string]any{"url": "chrome://sandbox"}, nil); err != nil {
		return "", err
	}
	in.settle(ctx, t, 10*time.Second)
	v, err := in.eval(ctx, t, `document.body ? document.body.innerText : ""`)
	if err != nil {
		return "", err
	}
	var s string
	_ = json.Unmarshal(v, &s)
	return s, nil
}

// SandboxedOK reads chrome://sandbox's verdict.
func SandboxedOK(status string) bool {
	return strings.Contains(status, "You are adequately sandboxed") &&
		!strings.Contains(status, "You are not adequately sandboxed")
}

// copyTo writes r to a new host file.
func copyTo(path string, r io.Reader) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
