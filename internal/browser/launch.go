package browser

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/browser/cdp"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// chromeCandidates are where FindChrome looks when the config names no
// binary: PATH names first, then the platform's application bundles.
var chromeCandidates = map[string][]string{
	"linux": {"google-chrome-stable", "google-chrome", "chromium", "chromium-browser", "chrome"},
	"darwin": {
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
	},
}

// FindChrome resolves the browser binary: configured, else the first
// candidate installed. It never looks in the operator's home — a Chrome
// there is theirs to name.
func FindChrome(configured string) (string, error) {
	return findChrome(configured, runtime.GOOS, exec.LookPath)
}

func findChrome(configured, goos string, lookPath func(string) (string, error)) (string, error) {
	if configured != "" {
		if !filepath.IsAbs(configured) {
			p, err := lookPath(configured)
			if err != nil {
				return "", fmt.Errorf("browser: chrome %q: %w", configured, err)
			}
			configured = p
		}
		if fi, err := os.Stat(configured); err != nil || fi.IsDir() {
			return "", fmt.Errorf("browser: chrome %s is not a file", configured)
		}
		return configured, nil
	}
	for _, c := range chromeCandidates[goos] {
		if filepath.IsAbs(c) {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
			continue
		}
		if p, err := lookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("browser: no Chrome or Chromium found (set browser.chrome to its path)")
}

// LaunchSpec is one browser process.
type LaunchSpec struct {
	// Chrome is the browser binary.
	Chrome string
	// Dir is the instance's private directory: the profile, the transfer
	// directory, its HOME. It is the only path the browser may write.
	Dir string
	// Allow is the egress allowlist; every request goes through conductor's
	// proxy with it, and there is no other way out.
	Allow []string
	// Headed opens a window (the operator's login takeover); Display then
	// carries the display's environment and Show the paths it needs.
	Headed  bool
	Display []string
	Show    []string
	// Args are further browser flags (a headed window's platform).
	Args []string
	// SelfExe is conductor's binary (the Linux in-sandbox forwarder).
	SelfExe func() (string, error)
	// Proxy serves the allowlist.
	Proxy *sandbox.ProxyManager
	// RealHome is the operator's home (macOS profile bookkeeping only; never
	// shown to the browser).
	RealHome string
	// Stderr receives the browser's own diagnostics (nil = discarded).
	Stderr *Ring
	// Engine / Image run the browser in a container (container.go) instead
	// of the host's sandbox.
	Engine *ContainerEngine
	Image  string
}

// Process is a running, sandboxed browser.
type Process struct {
	Conn *cdp.Conn
	cmd  *exec.Cmd
	done chan struct{}
	once sync.Once
	stop func()
	// Mux / Guest: a container browser's stream (file transfers ride it)
	// and its directory as it sees it; nil / "" for a host browser.
	Mux   *Mux
	Guest string
	// kill ends a container (its engine CLI going away does not).
	kill func()
}

// Isolation names how a browser is confined: "container", "namespace"
// (Linux), "seatbelt" (macOS without a container engine).
func (p *Process) Isolation() string {
	switch {
	case p.Mux != nil:
		return "container"
	case runtime.GOOS == "darwin":
		return "seatbelt"
	default:
		return "namespace"
	}
}

// Done is closed when the browser has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

// killProcess ends one browser process. It is a var so no test sends a real
// signal, and it refuses a pid <= 1: kill(-1) would reach every process the
// daemon's user owns, kill(0) its own group.
var killProcess = func(p *os.Process) error {
	if p == nil || p.Pid <= 1 {
		return fmt.Errorf("browser: refused to signal pid %d", pidOf(p))
	}
	return p.Kill()
}

func pidOf(p *os.Process) int {
	if p == nil {
		return 0
	}
	return p.Pid
}

// Close ends the browser: politely over the protocol, then by signal.
func (p *Process) Close() {
	p.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = p.Conn.Call(ctx, "", "Browser.close", nil, nil)
		cancel()
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			if p.kill != nil {
				p.kill()
			}
			if p.cmd.Process != nil {
				_ = killProcess(p.cmd.Process)
			}
			<-p.done
		}
		p.Conn.Close()
	})
}

// ChromeArgs are the browser's flags: no background traffic of its own, no
// password store, no extensions, every request through the proxy (loopback
// included), and the protocol on the pipe.
func ChromeArgs(dir, proxy string, headed bool) []string {
	args := []string{
		"--remote-debugging-pipe",
		"--user-data-dir=" + filepath.Join(dir, "profile"),
		"--disk-cache-dir=" + filepath.Join(dir, "cache"),
		"--proxy-server=http://" + proxy,
		// Chrome bypasses a proxy for loopback by default; nothing may.
		"--proxy-bypass-list=<-loopback>",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--disable-quic",
		"--no-first-run", "--no-default-browser-check", "--no-service-autorun",
		"--disable-default-apps", "--disable-extensions", "--disable-component-extensions-with-background-pages",
		"--disable-background-networking", "--disable-component-update", "--disable-sync",
		"--disable-domain-reliability", "--disable-client-side-phishing-detection",
		"--disable-breakpad", "--disable-crash-reporter", "--metrics-recording-only",
		"--password-store=basic", "--use-mock-keychain",
		"--disable-field-trial-config", "--no-pings", "--safebrowsing-disable-auto-update",
		"--disable-search-engine-choice-screen",
		// No background service of Chrome's own: none would get past the
		// proxy anyway, and password leak detection would send a filled
		// credential's hash prefix out.
		"--disable-features=Translate,OptimizationHints,OptimizationGuideModelDownloading,MediaRouter,DialMediaRouteProvider,AutofillServerCommunication,CertificateTransparencyComponentUpdater,InterestFeedContentSuggestions,PrivacySandboxSettings4,ChromeWhatsNewUI,PasswordLeakDetection,PushMessaging,NetworkTimeServiceQuerying,SafeBrowsingEnhancedProtectionMessageInInterstitials,LensOverlay,GlobalMediaControls,PreconnectToSearch,SearchPrefetchServicePrefetching,OmniboxSearchPrefetch,GCMUseDedicatedNetworkThread",
		"--disable-dev-shm-usage",
		"--window-size=1280,800",
		"--mute-audio",
	}
	if !headed {
		args = append(args, "--headless=new", "--disable-gpu", "--hide-scrollbars")
	}
	return append(args, "about:blank")
}

// Launch starts the browser sandboxed: on Linux in its own user, mount,
// pid and network namespaces with only Dir (and the browser's own install)
// visible, on macOS under a Seatbelt profile allowing the same. Its only
// network is conductor's proxy. The protocol runs over a pipe: there is no
// debugging port for anything else to reach.
func Launch(spec LaunchSpec) (*Process, error) {
	if spec.Engine != nil {
		return launchContainer(spec)
	}
	return launch(spec, sandbox.CheckGOOS)
}

func launch(spec LaunchSpec, goos string) (*Process, error) {
	if len(spec.Allow) == 0 {
		return nil, fmt.Errorf("browser: no site is allowed (browser.allow is empty)")
	}
	if spec.Proxy == nil || spec.SelfExe == nil {
		return nil, fmt.Errorf("browser: launcher not wired")
	}
	for _, d := range []string{"profile", "cache", "xfer", "home", "tmp"} {
		if err := os.MkdirAll(filepath.Join(spec.Dir, d), 0o700); err != nil {
			return nil, err
		}
	}
	var (
		proxyAddr string
		closeFn   func()
		err       error
	)
	deps := sandbox.LocalWrapDeps{SelfExe: spec.SelfExe, Confine: true, PassFDs: 2}
	var shortTmp string
	if goos == "darwin" {
		// Chromium's temp dir holds its singleton socket: short (sun_path is
		// ~104 bytes here), private, and the browser's own.
		if shortTmp, err = os.MkdirTemp("/tmp", "cbt"); err != nil {
			return nil, err
		}
		proxyAddr, closeFn, err = spec.Proxy.TrustedEndpoint(spec.Allow)
		if err != nil {
			os.RemoveAll(shortTmp)
			return nil, err
		}
		closeProxy := closeFn
		closeFn = func() { closeProxy(); os.RemoveAll(shortTmp) }
		deps.ExtraBinds = append(deps.ExtraBinds, sandbox.BindMount{Path: shortTmp})
		deps.EgressAddr = func([]string) (string, string, func(), error) { return proxyAddr, "", nil, nil }
		deps.Agent = seatbeltProfile(spec)
	} else {
		sock, c, err := spec.Proxy.TrustedUnixEndpoint(spec.Allow)
		if err != nil {
			return nil, err
		}
		closeFn, proxyAddr = c, sandbox.ForwardAddr
		deps.EgressUnix = func([]string) (string, string, func(), error) { return sock, "", nil, nil }
	}
	fail := func(err error) (*Process, error) {
		closeFn()
		return nil, err
	}
	deps.ExtraBinds = append(deps.ExtraBinds, sandbox.BindMount{Path: spec.Dir})
	if inst := installDir(spec.Chrome, goos); inst != "" {
		deps.ExtraBinds = append(deps.ExtraBinds, sandbox.BindMount{Path: inst, RO: true})
	}
	for _, p := range spec.Show {
		deps.ExtraBinds = append(deps.ExtraBinds, sandbox.BindMount{Path: p, RO: true})
	}
	env := browserEnv(spec, goos, shortTmp)
	flags := ChromeArgs(spec.Dir, proxyAddr, spec.Headed)
	argv := append(append([]string{spec.Chrome}, flags[:len(flags)-1]...), platformArgs(goos)...)
	if spec.Headed && goos == "darwin" {
		// The window renders in software: its profile opens no GPU class.
		argv = append(argv, "--disable-gpu")
	}
	argv = append(argv, spec.Args...)
	argv = append(argv, flags[len(flags)-1])
	wrapped, outEnv, wclean, err := sandbox.WrapLocalCommand(browserSpec(spec.Allow), argv, spec.Dir, env, deps)
	if err != nil {
		return fail(err)
	}
	stop := func() { wclean(); closeFn() }

	// fd 3: the browser reads commands; fd 4: it writes replies.
	cmdR, cmdW, err := os.Pipe()
	if err != nil {
		stop()
		return nil, err
	}
	repR, repW, err := os.Pipe()
	if err != nil {
		cmdR.Close()
		cmdW.Close()
		stop()
		return nil, err
	}
	cmd := exec.Command(wrapped[0], wrapped[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = outEnv
	cmd.ExtraFiles = []*os.File{cmdR, repW}
	if spec.Stderr != nil {
		cmd.Stdout, cmd.Stderr = spec.Stderr, spec.Stderr
	}
	setDeathSignal(cmd)
	if err := cmd.Start(); err != nil {
		cmdR.Close()
		cmdW.Close()
		repR.Close()
		repW.Close()
		stop()
		return nil, fmt.Errorf("browser: start: %w", err)
	}
	cmdR.Close()
	repW.Close()
	p := &Process{Conn: cdp.New(cmdW, repR), cmd: cmd, done: make(chan struct{}), stop: stop}
	go func() {
		_ = cmd.Wait()
		stop()
		close(p.done)
	}()
	// The browser is up when it answers (a cold start on a loaded box is slow).
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
		return nil, fmt.Errorf("browser: did not start: %v\n%s", err, msg)
	}
	return p, nil
}

// browserEnv is the browser's whole environment: none of the daemon's.
func browserEnv(spec LaunchSpec, goos, chromiumTmp string) []string {
	home := filepath.Join(spec.Dir, "home")
	env := []string{
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"LANG=en_US.UTF-8",
	}
	if goos == "darwin" {
		// CoreFoundation finds the home by the user database, not $HOME:
		// this points it (Application Support, preferences) into the
		// browser's own directory, never the operator's.
		// MAC_CHROMIUM_TMPDIR keeps Chromium's own temp files — its
		// process-singleton socket among them — out of the per-user temp
		// dir, where the operator's own Chrome keeps its singleton socket.
		tmp := filepath.Join(spec.Dir, "tmp")
		if chromiumTmp == "" {
			chromiumTmp = tmp
		}
		env = append(env, "TMPDIR="+tmp, "MAC_CHROMIUM_TMPDIR="+chromiumTmp, "CFFIXED_USER_HOME="+home)
	} else {
		env = append(env, "TMPDIR=/tmp")
	}
	if spec.Headed {
		env = append(env, spec.Display...)
	}
	return env
}

// installDir is the browser's install the sandbox must show read-only: the
// app bundle on macOS, the binary's directory elsewhere — "" when the base
// system already shows it (/usr, /opt).
func installDir(chrome, goos string) string {
	real := chrome
	if r, err := filepath.EvalSymlinks(chrome); err == nil {
		real = r
	}
	if goos == "darwin" {
		for d := real; d != "/" && d != "."; d = filepath.Dir(d) {
			if strings.HasSuffix(d, ".app") {
				return d
			}
		}
		return filepath.Dir(real)
	}
	for _, base := range []string{"/usr/", "/opt/"} {
		if strings.HasPrefix(real, base) {
			return ""
		}
	}
	return filepath.Dir(real)
}

// Ring keeps the last few KiB written to it (a browser's stderr).
type Ring struct {
	mu  sync.Mutex
	buf []byte
}

const ringMax = 16 << 10

func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > ringMax {
		r.buf = append([]byte(nil), r.buf[len(r.buf)-ringMax:]...)
	}
	r.mu.Unlock()
	return len(p), nil
}

func (r *Ring) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

// seatbeltProfile is the macOS browser's Seatbelt variant: the window
// server's services for the operator's headed window only.
func seatbeltProfile(spec LaunchSpec) *sandbox.AgentProfile {
	return &sandbox.AgentProfile{Browser: true, Window: spec.Headed, Home: spec.RealHome}
}

// browserSpec is the browser's sandbox: no network of its own (a network
// namespace on Linux, a Seatbelt deny on macOS), the allowlist proxy the
// only way out, and the pivot_root jail.
func browserSpec(allow []string) *sandbox.Spec {
	return &sandbox.Spec{Mode: "namespace", Deny: true, HasEgress: true, Egress: append([]string(nil), allow...)}
}

// platformArgs are the flags one platform's sandbox needs. macOS refuses a
// second Seatbelt sandbox inside a first (forbidden-sandbox-reinit), so
// Chromium's own per-process sandbox cannot nest in conductor's there: the
// outer profile confines every Chromium process alike. On Linux Chromium's
// namespace sandbox nests inside conductor's, and stays on.
func platformArgs(goos string) []string {
	if goos == "darwin" {
		return []string{"--no-sandbox"}
	}
	return nil
}
