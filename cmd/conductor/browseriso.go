package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/NodeSpy/conductor/internal/browser"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// The browser's isolation, decided once at boot (docs/wiki/Browser.md):
//
//   - Linux, container auto|off: the host's namespaces (Chromium's own
//     sandbox nests inside).
//   - container on, or auto on macOS with an engine present: a container,
//     Chromium's own sandbox on inside it. Until its image is built and
//     Chromium proves it is sandboxed there, browser actions are refused
//     ("being prepared") — never quietly run in the weaker mode.
//   - macOS without an engine (or whose container cannot sandbox Chromium,
//     under auto): Seatbelt alone, Chromium's own sandbox off — logged at
//     boot and with every session, with what it means.
//   - container on and impossible: every browser action refused.

// browserIso is the decided isolation (nil = the host's sandbox).
type browserIso struct {
	engine      *browser.ContainerEngine
	image       string
	note        string // the Seatbelt fallback's warning
	unavailable string
}

var browserIsoState atomic.Pointer[browserIso]

// applyIso folds the isolation into the settings.
func applyIso(st browser.Settings) browser.Settings {
	if iso := browserIsoState.Load(); iso != nil {
		st.Engine, st.Image, st.IsolationNote, st.Unavailable = iso.engine, iso.image, iso.note, iso.unavailable
	}
	return st
}

// decideBrowserIso decides, then prepares the container in the background.
func decideBrowserIso(ctx context.Context, b *config.BrowserConfig, logf func(string, ...any)) {
	isoDecide(ctx, b, runtime.GOOS, func() (*browser.ContainerEngine, error) {
		e, err := browser.FindEngine(b.ContainerEngine, runtime.GOOS, exec.LookPath, os.Environ())
		if err != nil {
			return nil, err
		}
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return e, e.Ping(pctx)
	}, prepareContainer, logf)
}

// prepareContainer builds the image and proves Chromium's sandbox in it.
func prepareContainer(ctx context.Context, e *browser.ContainerEngine, logf func(string, ...any)) (string, error) {
	img, err := e.EnsureImage(ctx, logf)
	if err == nil {
		err = containerSelfTest(ctx, e, img)
	}
	return img, err
}

// isoDecide is the decision, its seams injected (tests).
func isoDecide(ctx context.Context, b *config.BrowserConfig, goos string, find func() (*browser.ContainerEngine, error),
	prepare func(context.Context, *browser.ContainerEngine, func(string, ...any)) (string, error), logf func(string, ...any)) {
	mode := b.Container
	if mode == "" {
		mode = "auto"
	}
	if mode == "off" || (mode == "auto" && goos != "darwin") {
		browserIsoState.Store(&browserIso{note: seatbeltNote(goos, "browser.container: off")})
		if goos == "darwin" {
			logf("WARNING: browser: %s", seatbeltNote(goos, "browser.container: off"))
		} else {
			logf("browser: isolation: the host's namespaces, Chromium's own sandbox nested inside")
		}
		return
	}
	e, err := find()
	if err != nil {
		fallbackIso(mode, goos, err.Error(), logf)
		return
	}
	browserIsoState.Store(&browserIso{unavailable: fmt.Sprintf("the browser's container is being prepared (%s: its image builds on first use) — try again in a minute", e.Kind)})
	logf("browser: isolation: a container (%s) — preparing its image and proving Chromium's own sandbox runs inside", e.Kind)
	go func() {
		pctx, cancel := context.WithTimeout(ctx, 25*time.Minute)
		defer cancel()
		img, err := prepare(pctx, e, logf)
		if err != nil {
			fallbackIso(mode, goos, fmt.Sprintf("the %s container could not sandbox Chromium: %v", e.Kind, err), logf)
			return
		}
		browserIsoState.Store(&browserIso{engine: e, image: img})
		logf("browser: isolation: a container (%s, image %s) — Chromium's own sandbox on inside it (chrome://sandbox: adequately sandboxed)", e.Kind, img)
	}()
}

// fallbackIso is what happens without a usable container.
func fallbackIso(mode, goos, why string, logf func(string, ...any)) {
	if mode == "on" {
		browserIsoState.Store(&browserIso{unavailable: "browser.container: on, and " + why})
		logf("browser: OFF — browser.container: on, and %s", why)
		return
	}
	note := seatbeltNote(goos, why)
	browserIsoState.Store(&browserIso{note: note})
	logf("WARNING: browser: %s", note)
}

// containerSelfTest starts a container browser and reads Chromium's own
// sandbox report.
func containerSelfTest(ctx context.Context, e *browser.ContainerEngine, img string) error {
	dir, err := os.MkdirTemp("", "cbselftest")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	ring := &browser.Ring{}
	p, err := browser.Launch(browser.LaunchSpec{Dir: dir, Allow: []string{"selftest.invalid"}, Proxy: pm, Engine: e, Image: img, Stderr: ring})
	if err != nil {
		return err
	}
	defer p.Close()
	st, err := browser.SandboxStatus(ctx, p)
	if err != nil {
		return err
	}
	if !browser.SandboxedOK(st) {
		return fmt.Errorf("chrome://sandbox: %s", strings.Join(strings.Fields(st), " "))
	}
	return nil
}

// seatbeltNote says what the Seatbelt-only mode means, plainly.
func seatbeltNote(goos, why string) string {
	if goos != "darwin" {
		return ""
	}
	n := fmt.Sprintf("macOS without a container (%s): Chromium runs without its own sandbox, confined by Seatbelt alone — a renderer exploit holds the whole browser jail's reach. Its egress proxy is a loopback port any local user could relay through to the allowlist: run this mode on a single-user Mac only (or install Docker Desktop, colima or podman)", why)
	if u := localUsers(); u > 1 {
		n += fmt.Sprintf(" — THIS MAC HAS %d LOCAL USERS", u)
	}
	return n
}

// localUsers counts the Mac's human accounts (uid ≥ 501, not _system).
func localUsers() int {
	out, err := exec.Command("dscl", ".", "-list", "/Users", "UniqueID").Output()
	if err != nil {
		return 0
	}
	return countHumanUsers(string(out))
}

func countHumanUsers(dscl string) int {
	n := 0
	for _, l := range strings.Split(dscl, "\n") {
		f := strings.Fields(l)
		if len(f) != 2 || strings.HasPrefix(f[0], "_") {
			continue
		}
		if uid, err := strconv.Atoi(f[1]); err == nil && uid >= 501 && uid < 100000 {
			n++
		}
	}
	return n
}
