package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/browser"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/notify"
	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/skill"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// The daemon's browser (internal/browser, docs/wiki/Browser.md): wired at
// boot when the config has a browser: section and the agent sandbox is on.

var (
	browserMgr atomic.Pointer[browser.Manager]
	browserCfg atomic.Pointer[config.Config]
)

// requesterFor is who asked for a session's work, as the browser sees it:
// the token's own requester. Members (#178) replace it with their case
// provenance (the member gate's requester class, read at every action).
var requesterFor = func(id skill.Identity) string { return id.Requester }

// browserSettings resolves the browser: section.
func browserSettings(cfg *config.Config) browser.Settings {
	b := cfg.Browser
	if b == nil {
		return browser.Settings{}
	}
	st := browser.Settings{
		Chrome: b.Chrome, Evaluate: []string(b.Evaluate), IdleTimeout: b.BrowserIdle(), MaxBrowsers: b.MaxBrowsers,
		MaxDownload: int64(b.MaxDownloadMB) << 20, MaxUpload: int64(b.MaxUploadMB) << 20,
	}
	if st.MaxBrowsers == 0 {
		st.MaxBrowsers = 4
	}
	if st.MaxDownload == 0 {
		st.MaxDownload = 100 << 20
	}
	if st.MaxUpload == 0 {
		st.MaxUpload = 25 << 20
	}
	st.Allow = append(st.Allow, b.Allow...)
	if cfg.Sandbox != nil {
		st.Allow = append(st.Allow, cfg.Sandbox.Egress...)
	}
	for _, l := range b.Logins {
		st.Logins = append(st.Logins, browser.LoginSiteConfig{LoginSite: browser.LoginSite{Site: l.Site, Identities: l.Identities}, Also: l.Also})
	}
	ref := func(r *config.BrowserSecretRef) *browser.SecretRef {
		if r == nil {
			return nil
		}
		return &browser.SecretRef{Vault: r.Vault, Key: r.Key}
	}
	for _, c := range b.Credentials {
		st.Credentials = append(st.Credentials, browser.Credential{
			Name: c.Name, Origin: c.Origin, Identities: c.Identities, LoginURL: c.LoginURL, Approval: c.Approval,
			Username: ref(c.Username), Password: ref(c.Password), OTP: ref(c.OTP), Passkey: ref(c.Passkey),
		})
	}
	return st
}

// wireBrowser turns the browser on for this daemon.
func wireBrowser(ctx context.Context, cfg *config.Config, notifier *notify.Notifier, audit func(map[string]any), logf func(string, ...any)) func() {
	browserCfg.Store(cfg)
	if !cfg.BrowserOn() {
		memory.SetBrowserOp(nil)
		browserMgr.Store(nil)
		return func() {}
	}
	if !cfg.AgentSandboxOn() {
		logf("browser: off — it serves sandboxed agents only, and sandbox.agents is off")
		memory.SetBrowserOp(nil)
		return func() {}
	}
	home, _ := os.UserHomeDir()
	m := &browser.Manager{
		Settings: func() browser.Settings { return browserSettings(browserCfg.Load()) },
		StateDir: cfg.BrowserStateDir(),
		SelfExe:  os.Executable,
		RealHome: home,
		Audit:    audit,
		Log:      logf,
		Secrets:  vaults.ReadFresh,
		PutSecret: func(ctx context.Context, vault, key, value string) error {
			return vaults.Write(ctx, vault, key, value)
		},
		Notify: func(msg string) {
			if notifier != nil {
				notifier.Emit(context.Background(), notify.EventNeedsInput, core.Trigger{Kind: "browser", Title: msg}, msg)
			}
		},
	}
	if err := os.MkdirAll(m.StateDir, 0o700); err != nil {
		logf("browser: off — state dir %s: %v", m.StateDir, err)
		return func() {}
	}
	m.SweepRun()
	rctx, cancel := context.WithCancel(ctx)
	go m.ReapLoop(rctx, 30*time.Second)
	browserMgr.Store(m)
	memory.SetBrowserOp(func(ctx context.Context, token, action string, args map[string]any, peer memory.Peer) (map[string]any, error) {
		b := skill.Active()
		if b == nil {
			return nil, fmt.Errorf("browser: no skill broker on this daemon")
		}
		id, err := b.Authorize(token, skill.Peer{PID: peer.PID, StartTime: peer.StartTime, UID: peer.UID, Valid: peer.Valid})
		if err != nil {
			return nil, err
		}
		c, err := browserCaller(token, id)
		if err != nil {
			return nil, err
		}
		return m.Do(ctx, c, action, args)
	})
	if _, err := browser.FindChrome(cfg.Browser.Chrome); err != nil {
		logf("WARNING: browser: %v — every browser action will fail until one is installed", err)
	}
	logf("browser: on (%d allowed site pattern(s), %d login site(s), %d credential(s), read-only evaluate for %d identity pattern(s))",
		len(cfg.Browser.Allow), len(cfg.Browser.Logins), len(cfg.Browser.Credentials), len(cfg.Browser.Evaluate))
	return func() {
		memory.SetBrowserOp(nil)
		browserMgr.Store(nil)
		cancel()
		m.Close()
	}
}

// browserCaller is what a token stands for, for the browser: its sandbox
// (bound into the token at mint, else its dispatch's registered workspace),
// its provenance, and a session of its own.
func browserCaller(token string, id skill.Identity) (browser.Principal, error) {
	c := browser.Principal{Key: browser.SessionKey(token), Agent: id.Agent, Repo: id.Repo, Dispatch: id.Dispatch, Requester: requesterFor(id)}
	switch {
	case id.Sandbox != nil:
		c.Identity, c.Workspace, c.Egress, c.Deny = id.Sandbox.Identity, id.Sandbox.Workspace, id.Sandbox.Egress, id.Sandbox.Deny
	case id.Dispatch != "":
		e, ok := agentsandbox.Default.ForDispatch(id.Dispatch)
		if !ok {
			return c, fmt.Errorf("browser: this session's workspace is not registered with the sandbox — conductor's browser serves sandboxed agents")
		}
		c.Identity, c.Workspace, c.Egress = e.Identity, e.Workspace, e.Egress
		c.Deny = append(append([]string(nil), e.ReadOnly...), e.Hide...)
	default:
		return c, fmt.Errorf("browser: this session has no sandbox identity — conductor's browser serves sandboxed agents")
	}
	return c, nil
}

// browserControl answers the operator's browser commands on the control
// socket (same uid, as every control command).
func browserControl(conn net.Conn, req controlRequest) controlResponse {
	if uid, ok := agentsandbox.PeerUID(conn); ok && int(uid) != os.Getuid() {
		return controlResponse{Error: fmt.Sprintf("browser: refused for uid %d", uid)}
	}
	cfg := browserCfg.Load()
	m := browserMgr.Load()
	if cfg == nil || !cfg.BrowserOn() || m == nil {
		return controlResponse{Error: "browser: not configured on this daemon (no browser: section, or the agent sandbox is off)"}
	}
	st := browserSettings(cfg)
	audit := func(e map[string]any) {
		if m.Audit != nil {
			e["event"], e["by"] = "browser", "operator"
			m.Audit(e)
		}
	}
	data := func(v any) controlResponse {
		b, _ := json.Marshal(v)
		return controlResponse{OK: true, Data: b}
	}
	loginSite := func() (browser.LoginSiteConfig, error) {
		for _, l := range st.Logins {
			if l.Site == req.Site {
				if !l.For(req.Identity) {
					return l, fmt.Errorf("browser: %s is not opted in for %s (browser.logins)", req.Site, req.Identity)
				}
				return l, nil
			}
		}
		return browser.LoginSiteConfig{}, fmt.Errorf("browser: %s is not a login site (browser.logins)", req.Site)
	}
	credential := func() (browser.Credential, error) {
		for _, c := range st.Credentials {
			if c.Name == req.Credential {
				return c, nil
			}
		}
		return browser.Credential{}, fmt.Errorf("browser: no credential %q (browser.credentials)", req.Credential)
	}
	switch req.Cmd {
	case "browser-logins":
		jars, err := m.Logins().List()
		if err != nil {
			return controlResponse{Error: err.Error()}
		}
		type credInfo struct {
			Name, Origin string
			Identities   []string
			Fields       []string
		}
		var creds []credInfo
		for _, c := range st.Credentials {
			ci := credInfo{Name: c.Name, Origin: c.Origin, Identities: c.Identities}
			for _, f := range []struct {
				name string
				ref  *browser.SecretRef
			}{{"username", c.Username}, {"password", c.Password}, {"otp", c.OTP}, {"passkey", c.Passkey}} {
				if f.ref != nil {
					ci.Fields = append(ci.Fields, f.name)
				}
			}
			creds = append(creds, ci)
		}
		return data(map[string]any{"sessions": jars, "login_sites": st.Logins, "credentials": creds})
	case "browser-cookies-save":
		if _, err := loginSite(); err != nil {
			return controlResponse{Error: err.Error()}
		}
		cs := browser.FilterCookies(req.Site, req.Cookies, time.Now())
		if len(cs) == 0 {
			return controlResponse{Error: fmt.Sprintf("browser: none of those cookies is for %s — nothing saved", req.Site)}
		}
		src := req.Source
		if src == "" {
			src = "import"
		}
		if err := m.Logins().Save(browser.Jar{Identity: req.Identity, Site: req.Site, Source: src, Cookies: cs}); err != nil {
			return controlResponse{Error: err.Error()}
		}
		audit(map[string]any{"action": "session-save", "identity": req.Identity, "site": req.Site, "cookies": len(cs), "source": src, "outcome": "ok"})
		return controlResponse{OK: true, Msg: fmt.Sprintf("saved %d cookie(s) for %s as %s's session (%d of the %d given were for other sites and dropped)", len(cs), req.Site, req.Identity, len(req.Cookies)-len(cs), len(req.Cookies))}
	case "browser-logout":
		ok, err := m.Logins().Delete(req.Identity, req.Site)
		if err != nil {
			return controlResponse{Error: err.Error()}
		}
		audit(map[string]any{"action": "session-delete", "identity": req.Identity, "site": req.Site, "outcome": map[bool]string{true: "ok", false: "none"}[ok]})
		return controlResponse{OK: true, Msg: map[bool]string{true: "removed the saved session", false: "there was no saved session"}[ok]}
	case "browser-sessions":
		return data(m.Sessions())
	case "browser-peek":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		png, u, err := m.Peek(ctx, req.Session)
		if err != nil {
			return controlResponse{Error: err.Error()}
		}
		return data(map[string]any{"png": base64.StdEncoding.EncodeToString(png), "url": u})
	case "browser-takeover":
		chrome, err := browser.FindChrome(st.Chrome)
		if err != nil {
			return controlResponse{Error: err.Error()}
		}
		plan := map[string]any{"chrome": chrome}
		switch {
		case req.Credential != "":
			c, err := credential()
			if err != nil {
				return controlResponse{Error: err.Error()}
			}
			if c.Passkey == nil {
				return controlResponse{Error: fmt.Sprintf("browser: credential %q has no passkey: (where to keep it)", c.Name)}
			}
			host, err := browser.CredentialHost(c.Origin)
			if err != nil {
				return controlResponse{Error: err.Error()}
			}
			plan["host"], plan["origin"], plan["allow"] = host, c.Origin, st.Allow
			plan["identity"] = c.Identities[0]
			req.Identity, req.Site = c.Identities[0], host
			// Continue from the identity's saved session on the site, if any.
			for _, l := range st.Logins {
				if !l.For(c.Identities[0]) || !browser.SiteCovers(l.Site, host) {
					continue
				}
				if j, ok, _ := m.Logins().Load(c.Identities[0], l.Site); ok {
					plan["cookies"] = j.Cookies
				}
			}
		default:
			l, err := loginSite()
			if err != nil {
				return controlResponse{Error: err.Error()}
			}
			plan["allow"] = browser.LoginAllow(st, []string{l.Site})
			if j, ok, _ := m.Logins().Load(req.Identity, req.Site); ok {
				plan["cookies"] = j.Cookies
			}
		}
		audit(map[string]any{"action": "takeover", "identity": req.Identity, "site": req.Site, "credential": req.Credential, "outcome": "ok"})
		return data(plan)
	case "browser-passkey-save":
		c, err := credential()
		if err != nil || c.Passkey == nil || req.Passkey == nil {
			if err == nil {
				err = fmt.Errorf("browser: nothing to save")
			}
			return controlResponse{Error: err.Error()}
		}
		host, _ := browser.CredentialHost(c.Origin)
		if !browser.RPFits(req.Passkey.RPID, host) {
			return controlResponse{Error: fmt.Sprintf("browser: the passkey is for %s, not %s", req.Passkey.RPID, host)}
		}
		b, _ := json.Marshal(req.Passkey)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := vaults.Write(ctx, c.Passkey.Vault, c.Passkey.Key, string(b)); err != nil {
			return controlResponse{Error: err.Error()}
		}
		audit(map[string]any{"action": "passkey-save", "credential": c.Name, "identity": c.Identities[0], "rp": req.Passkey.RPID, "outcome": "ok"})
		return controlResponse{OK: true, Msg: fmt.Sprintf("saved the passkey for %s (rp %s) in vault %s", c.Name, req.Passkey.RPID, c.Passkey.Vault)}
	}
	return controlResponse{Error: "browser: unknown command " + req.Cmd}
}

const browserUsage = `usage: conductor browser <action> [args]

An agent's browser (inside a sandboxed agent; over CONDUCTOR_ENDPOINT):
  open URL                      load a page (only browser.allow sites)
  snapshot [--max N]            the page as text, each control with a ref (e12)
  click REF                     click
  fill REF TEXT [--submit]      replace a field's text
  type TEXT [--ref REF] [--submit]  type at a field (or the focus)
  select REF VALUE              pick a <select> option by value or text
  press KEY [--ref REF]         Enter, Tab, Escape, Control+a, …
  scroll [--ref REF] [--dy PX]  bring an element into view, or scroll
  wait [--text T] [--gone T] [--url U] [--timeout S]
  screenshot [--path F.png] [--full]   written into the workspace
  evaluate EXPR                 read-only JavaScript (when browser.evaluate allows)
  back | forward | reload | tabs | downloads | status | close
  tab new [URL] | tab select N | tab close N
  upload REF PATH...            files from the workspace
  login --credential NAME [--passkey --ref REF]   conductor signs you in; you never see a value
  fill-credential REF FIELD --credential NAME     FIELD: username | password | otp
  await-approval --credential NAME [--timeout S]  wait while the operator approves on their phone

The operator's (on the daemon's control socket):
  logins                                   saved sessions, login sites, credentials (no values)
  seed --identity ID --site SITE [--url U] [--headless --wait-url SUBSTR]
                                           log in yourself in a browser window; its session is saved
  cookies import --identity ID --site SITE FILE   a JSON or cookies.txt export — only SITE's kept
  logout --identity ID --site SITE         drop a saved session
  sessions                                 live agent browsers
  peek SESSION [--out FILE.png]            watch one: a screenshot
  passkey register CREDENTIAL [--url U] [--headless]
                                           register a dedicated passkey for CREDENTIAL on its site
  --config PATH / --socket PATH            which daemon
`

// cmdBrowser is `conductor browser …`.
func cmdBrowser(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(os.Stderr, browserUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	var err error
	switch args[0] {
	case "logins", "seed", "cookies", "logout", "sessions", "peek", "passkey":
		err = browserOperator(args[0], args[1:])
	default:
		err = browserAgent(args[0], args[1:])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "conductor browser %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

// splitBrowserArgs separates positionals from --flags (a bare flag is true).
func splitBrowserArgs(args []string) (pos []string, flags map[string]any) {
	flags = map[string]any{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "--") || len(a) == 2 {
			pos = append(pos, a)
			continue
		}
		k := strings.TrimPrefix(a, "--")
		if key, v, ok := strings.Cut(k, "="); ok {
			flags[key] = v
			continue
		}
		switch k {
		case "submit", "full", "passkey", "headless":
			flags[k] = true
		default:
			if i+1 < len(args) {
				i++
				flags[k] = args[i]
			} else {
				flags[k] = true
			}
		}
	}
	return pos, flags
}

// browserAgent runs one agent action through the daemon.
func browserAgent(action string, rest []string) error {
	pos, flags := splitBrowserArgs(rest)
	a := map[string]any{}
	for k, v := range flags {
		a[k] = v
	}
	need := func(n int, usage string) error {
		if len(pos) < n {
			return fmt.Errorf("usage: conductor browser %s %s", action, usage)
		}
		return nil
	}
	switch action {
	case "open", "navigate", "goto":
		action = "open"
		if err := need(1, "URL"); err != nil {
			return err
		}
		a["url"] = pos[0]
	case "click":
		if err := need(1, "REF"); err != nil {
			return err
		}
		a["ref"] = pos[0]
	case "fill":
		if err := need(2, "REF TEXT"); err != nil {
			return err
		}
		a["ref"], a["text"] = pos[0], strings.Join(pos[1:], " ")
	case "type":
		if err := need(1, "TEXT [--ref REF]"); err != nil {
			return err
		}
		a["text"] = strings.Join(pos, " ")
	case "select":
		if err := need(2, "REF VALUE"); err != nil {
			return err
		}
		a["ref"], a["value"] = pos[0], strings.Join(pos[1:], " ")
	case "press":
		if err := need(1, "KEY [--ref REF]"); err != nil {
			return err
		}
		a["key"] = pos[0]
	case "evaluate", "eval":
		action = "evaluate"
		if err := need(1, "EXPR"); err != nil {
			return err
		}
		a["expr"] = strings.Join(pos, " ")
	case "upload":
		if err := need(2, "REF PATH..."); err != nil {
			return err
		}
		a["ref"] = pos[0]
		var ps []any
		for _, p := range pos[1:] {
			ps = append(ps, p)
		}
		a["paths"] = ps
	case "fill-credential":
		if err := need(2, "REF FIELD --credential NAME"); err != nil {
			return err
		}
		a["ref"], a["field"] = pos[0], pos[1]
	case "tab":
		if err := need(1, "new [URL] | select N | close N"); err != nil {
			return err
		}
		switch pos[0] {
		case "new":
			action = "tab-new"
			if len(pos) > 1 {
				a["url"] = pos[1]
			}
		case "select", "close":
			action = "tab-" + pos[0]
			if len(pos) < 2 {
				return fmt.Errorf("usage: conductor browser tab %s N", pos[0])
			}
			n, err := strconv.Atoi(pos[1])
			if err != nil {
				return fmt.Errorf("tab %s: %q is not a tab number", pos[0], pos[1])
			}
			a["tab"] = float64(n)
		default:
			return fmt.Errorf("usage: conductor browser tab new [URL] | select N | close N")
		}
	}
	for _, k := range []string{"dy", "timeout", "max"} {
		if v, ok := a[k].(string); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				a[k] = f
			}
		}
	}
	resp, err := skillCall(memory.IPCRequest{Op: "browser", Uses: action, Options: a})
	if err != nil {
		return err
	}
	if snap, ok := resp.Result["snapshot"].(string); ok && action == "snapshot" {
		fmt.Printf("%s — %v\n%s", resp.Result["title"], resp.Result["url"], snap)
		if p, ok := resp.Result["profile"].(string); ok && p != "anonymous" {
			fmt.Printf("(profile: %s)\n", p)
		}
		return nil
	}
	return printJSON(resp.Result)
}

// browserCtl sends one operator command to the daemon.
func browserCtl(flags map[string]any, req controlRequest) (controlResponse, error) {
	o := sandboxExecOpts{}
	if v, ok := flags["config"].(string); ok {
		o.config = v
	}
	if v, ok := flags["socket"].(string); ok {
		o.socket = v
	}
	sock := sandboxControlSocket(o)
	conn, err := net.DialTimeout("unix", sock, 10*time.Second)
	if err != nil {
		return controlResponse{}, fmt.Errorf("the conductor daemon is unreachable at %s (%v)", sock, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return controlResponse{}, err
	}
	var resp controlResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return controlResponse{}, err
	}
	if resp.Error != "" {
		return resp, fmt.Errorf("%s", resp.Error)
	}
	return resp, nil
}

func flagStr(flags map[string]any, k string) string { v, _ := flags[k].(string); return v }

// browserOperator runs the operator's commands.
func browserOperator(cmd string, rest []string) error {
	pos, flags := splitBrowserArgs(rest)
	switch cmd {
	case "logins", "sessions":
		resp, err := browserCtl(flags, controlRequest{Cmd: "browser-" + cmd})
		if err != nil {
			return err
		}
		var v any
		_ = json.Unmarshal(resp.Data, &v)
		return printJSON(v)
	case "logout":
		resp, err := browserCtl(flags, controlRequest{Cmd: "browser-logout", Identity: flagStr(flags, "identity"), Site: flagStr(flags, "site")})
		if err != nil {
			return err
		}
		fmt.Println(resp.Msg)
		return nil
	case "cookies":
		if len(pos) != 2 || pos[0] != "import" {
			return fmt.Errorf("usage: conductor browser cookies import --identity ID --site SITE FILE")
		}
		b, err := os.ReadFile(pos[1])
		if err != nil {
			return err
		}
		cs, err := browser.ParseCookies(b)
		if err != nil {
			return err
		}
		resp, err := browserCtl(flags, controlRequest{Cmd: "browser-cookies-save", Identity: flagStr(flags, "identity"), Site: flagStr(flags, "site"), Cookies: cs, Source: "import"})
		if err != nil {
			return err
		}
		fmt.Println(resp.Msg)
		return nil
	case "peek":
		if len(pos) != 1 {
			return fmt.Errorf("usage: conductor browser peek SESSION [--out FILE.png]")
		}
		resp, err := browserCtl(flags, controlRequest{Cmd: "browser-peek", Session: pos[0]})
		if err != nil {
			return err
		}
		var v struct{ PNG, URL string }
		_ = json.Unmarshal(resp.Data, &v)
		png, _ := base64.StdEncoding.DecodeString(v.PNG)
		out := flagStr(flags, "out")
		if out == "" {
			out = "peek-" + pos[0] + ".png"
		}
		if err := os.WriteFile(out, png, 0o600); err != nil {
			return err
		}
		fmt.Printf("%s: %s\n", v.URL, out)
		return nil
	case "seed":
		return browserTakeover(flags, controlRequest{Cmd: "browser-takeover", Identity: flagStr(flags, "identity"), Site: flagStr(flags, "site")}, false)
	case "passkey":
		if len(pos) != 2 || pos[0] != "register" {
			return fmt.Errorf("usage: conductor browser passkey register CREDENTIAL [--url U] [--headless]")
		}
		return browserTakeover(flags, controlRequest{Cmd: "browser-takeover", Credential: pos[1]}, true)
	}
	return fmt.Errorf("unknown command")
}

// browserTakeover runs the operator's own browser — on their display
// unless --headless — sandboxed and allowlisted, to seed a session or
// register a passkey; only that is kept.
func browserTakeover(flags map[string]any, req controlRequest, passkey bool) error {
	resp, err := browserCtl(flags, req)
	if err != nil {
		return err
	}
	var plan struct {
		Chrome, Host, Origin, Identity string
		Allow                          []string
		Cookies                        []browser.Cookie
	}
	if err := json.Unmarshal(resp.Data, &plan); err != nil {
		return err
	}
	headed := flags["headless"] != true
	spec := browser.LaunchSpec{Chrome: plan.Chrome, Allow: plan.Allow, SelfExe: os.Executable, Headed: headed, Stderr: &browser.Ring{}}
	if headed {
		spec.Display, spec.Show, spec.Args, err = displayFor(runtime.GOOS)
		if err != nil {
			return err
		}
	}
	dir, err := os.MkdirTemp("", "cbk")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	spec.Dir = dir
	spec.Proxy = sandbox.NewProxyManager(func(_, hp string) {
		fmt.Fprintf(os.Stderr, "(the browser was refused %s: not in browser.allow)\n", hp)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	k, err := browser.StartTakeover(ctx, spec, plan.Cookies)
	if err != nil {
		return err
	}
	defer k.Close()
	url := flagStr(flags, "url")
	if passkey {
		auth, err := k.ArmPasskey(ctx)
		if err != nil {
			return err
		}
		if url == "" {
			url = plan.Origin
		}
		if err := k.Open(ctx, url); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Add a passkey on %s in this browser (sign in first if you need to). It is conductor's own passkey for %s — revocable on the site without touching yours. Waiting for the site to create it…\n", plan.Origin, plan.Identity)
		enter := enterPressed(headed)
		pk, err := k.TakePasskey(ctx, auth, plan.Host, enter)
		if err != nil {
			return err
		}
		// The authenticator holds it before the site has it: let the page
		// finish telling the site.
		if headed {
			fmt.Fprintf(os.Stderr, "The passkey was created. Finish on the site (it should list the new passkey), then press Enter to save it.\n")
			<-enter
		} else {
			time.Sleep(3 * time.Second)
		}
		r2, err := browserCtl(flags, controlRequest{Cmd: "browser-passkey-save", Credential: req.Credential, Passkey: &pk})
		if err != nil {
			return err
		}
		fmt.Println(r2.Msg)
		return nil
	}
	if url == "" {
		url = "https://" + strings.TrimPrefix(req.Site, "*.") + "/"
	}
	if err := k.Open(ctx, url); err != nil {
		fmt.Fprintf(os.Stderr, "(%v)\n", err)
	}
	if w := flagStr(flags, "wait-url"); w != "" {
		fmt.Fprintf(os.Stderr, "Log in to %s; waiting for a page whose URL contains %q…\n", req.Site, w)
		for !strings.Contains(k.URL(ctx), w) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "Log in to %s in the window, then press Enter here. Only %s's cookies are kept, as %s's saved session — opting it in gives that identity's agent your session's power on %s for your own requests.\n", req.Site, req.Site, req.Identity, req.Site)
		<-enterPressed(true)
	}
	cs, err := k.Cookies(ctx, req.Site)
	if err != nil {
		return err
	}
	r2, err := browserCtl(flags, controlRequest{Cmd: "browser-cookies-save", Identity: req.Identity, Site: req.Site, Cookies: cs, Source: "login"})
	if err != nil {
		return err
	}
	fmt.Println(r2.Msg)
	return nil
}

// enterPressed closes when the operator presses Enter (never, when on is
// false: a headless run is scripted).
func enterPressed(on bool) <-chan struct{} {
	ch := make(chan struct{})
	if on {
		go func() {
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
			close(ch)
		}()
	}
	return ch
}

// displayFor is what a headed browser needs to show a window: the display's
// environment, the sockets to show it (read-only), and Chrome's platform
// flag. macOS's window server is reached by Mach service.
func displayFor(goos string) (env, show, args []string, err error) {
	if goos == "darwin" {
		return nil, nil, nil, nil
	}
	if d := os.Getenv("DISPLAY"); d != "" {
		env = append(env, "DISPLAY="+d)
		if _, err := os.Stat("/tmp/.X11-unix"); err == nil {
			show = append(show, "/tmp/.X11-unix")
		}
		if xa := os.Getenv("XAUTHORITY"); xa != "" {
			env, show = append(env, "XAUTHORITY="+xa), append(show, xa)
		}
		return env, show, []string{"--ozone-platform=x11"}, nil
	}
	if w := os.Getenv("WAYLAND_DISPLAY"); w != "" {
		rt := os.Getenv("XDG_RUNTIME_DIR")
		sock := w
		if !filepath.IsAbs(sock) {
			sock = filepath.Join(rt, w)
		}
		return []string{"WAYLAND_DISPLAY=" + sock}, []string{sock}, []string{"--ozone-platform=wayland"}, nil
	}
	return nil, nil, nil, fmt.Errorf("no display (DISPLAY / WAYLAND_DISPLAY) — run it on your desktop, or --headless with --wait-url for a scripted site")
}
