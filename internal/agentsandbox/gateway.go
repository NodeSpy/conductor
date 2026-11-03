package agentsandbox

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// The model gateway. A sandboxed agent never holds the operator's model API
// key: its model endpoint is a conductor-served HTTP gateway (relayed into
// the Linux sandbox like a loopback model router; a host loopback port the
// macOS profile allows), and its key a random token minted for the launch.
// The gateway accepts a request only with that token, removes it, adds the
// operator's real credential, and forwards to the one upstream the operator
// configured (their router, or the provider's API). What leaks out of a
// compromised agent is a token that dies with the launch.

// Model credential modes (sandbox.model_credential).
const (
	// CredGateway (the default): API keys stay on the host behind the
	// gateway; a login file is shared as an access-token-only copy.
	CredGateway = "gateway"
	// CredLoginCopy: no gateway — API keys in the environment reach the
	// agent; login files are access-token-only copies.
	CredLoginCopy = "login-copy"
	// CredLoginShare: the operator's login files, refresh tokens included,
	// shared read-only, and API keys passed through: the pre-gateway
	// behavior, minus write access to the login.
	CredLoginShare = "login-share"
)

// modelRoute is one model API a tool reaches: the variable that overrides
// its base URL, the variables it takes a key from (and the header each is
// sent in), the variable the agent's token goes in, and the default
// upstream.
type modelRoute struct {
	baseEnv  string
	keys     []routeKey
	agentKey string
	upstream string
}

type routeKey struct {
	env    string
	header string // "x-api-key", "bearer" or "x-goog-api-key"
}

var (
	anthropicRoute = modelRoute{baseEnv: "ANTHROPIC_BASE_URL",
		keys: []routeKey{{"ANTHROPIC_API_KEY", "x-api-key"}, {"ANTHROPIC_AUTH_TOKEN", "bearer"}},
		// A bearer token: claude-code asks nobody to approve it (an
		// ANTHROPIC_API_KEY it has not seen prompts in its interactive UI).
		agentKey: "ANTHROPIC_AUTH_TOKEN", upstream: "https://api.anthropic.com"}
	openaiRoute = modelRoute{baseEnv: "OPENAI_BASE_URL",
		keys:     []routeKey{{"OPENAI_API_KEY", "bearer"}, {"CODEX_API_KEY", "bearer"}},
		agentKey: "OPENAI_API_KEY", upstream: "https://api.openai.com/v1"}
	geminiRoute = modelRoute{baseEnv: "GOOGLE_GEMINI_BASE_URL",
		keys:     []routeKey{{"GEMINI_API_KEY", "x-goog-api-key"}, {"GOOGLE_API_KEY", "x-goog-api-key"}},
		agentKey: "GEMINI_API_KEY", upstream: "https://generativelanguage.googleapis.com"}
	qwenRoute = modelRoute{baseEnv: "OPENAI_BASE_URL",
		keys:     []routeKey{{"OPENAI_API_KEY", "bearer"}, {"DASHSCOPE_API_KEY", "bearer"}},
		agentKey: "OPENAI_API_KEY", upstream: "https://dashscope.aliyuncs.com/compatible-mode/v1"}
)

// toolRoutes are the gatewayed model APIs per tool. opencode configures
// its providers in its own config files and is not gatewayed (Isolation.md).
var toolRoutes = map[string][]modelRoute{
	"claude-code": {anthropicRoute},
	"codex":       {openaiRoute},
	"gemini":      {geminiRoute},
	"pi":          {anthropicRoute, openaiRoute},
	"qwen":        {qwenRoute},
}

// gatewayPlan is one route's gateway for a launch.
type gatewayPlan struct {
	route    modelRoute
	upstream *url.URL
	header   string // how the real key is sent ("" = no key: a router that needs none)
	key      string
	token    string
	loopback string // the upstream's host:port when it is a host loopback service
}

// planGateways decides which of tool's routes a launch gateways: each one
// with a known key, and each whose upstream is on the host loopback (a
// local router — reached through conductor, never directly). env is the
// launching process's environment; settings is claude-code's settings.json
// env block from the operator's home (nil: none).
func planGateways(tool string, env []string, settings map[string]string) ([]gatewayPlan, []modelRoute, error) {
	vars := map[string]string{}
	for k, v := range settings {
		vars[k] = v
	}
	for _, kv := range env { // the process's own environment wins
		if k, v, ok := strings.Cut(kv, "="); ok && v != "" {
			vars[k] = v
		}
	}
	var plans []gatewayPlan
	var left []modelRoute
	for _, r := range toolRoutes[tool] {
		base := vars[r.baseEnv]
		if base == "" {
			base = r.upstream
		}
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			left = append(left, r)
			continue
		}
		g := gatewayPlan{route: r, upstream: u}
		for _, k := range r.keys {
			if v := vars[k.env]; v != "" {
				g.header, g.key = k.header, v
				break
			}
		}
		if h := u.Hostname(); isLoopbackHost(h) {
			port := u.Port()
			if port == "" {
				port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
			}
			if h == "localhost" {
				h = "127.0.0.1"
			}
			g.loopback = net.JoinHostPort(h, port)
		}
		if g.key == "" && g.loopback == "" {
			left = append(left, r) // an OAuth login: the provider directly
			continue
		}
		tok, err := gatewayToken()
		if err != nil {
			return nil, nil, err
		}
		g.token = tok
		plans = append(plans, g)
	}
	return plans, left, nil
}

func gatewayToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "cgw-" + hex.EncodeToString(b), nil
}

// Default model gateway caps: per launch, both ways. Generous — a model
// client resends its whole context with every request — but finite.
const (
	DefaultModelUpload   = 16 << 30
	DefaultModelDownload = 16 << 30
)

// modelMeter counts one launch's model traffic, all of its gateways
// together, against its caps (<= 0 after defaults: none). event (nil:
// none) receives a model_request row per request and one limit event when
// a cap is first passed.
type modelMeter struct {
	run            string
	capUp, capDown int64
	up, down       atomic.Int64
	capped         sync.Once
	event          func(string, map[string]any)
}

func newModelMeter(run string, up, down int64, event func(string, map[string]any)) *modelMeter {
	if up == 0 {
		up = DefaultModelUpload
	}
	if down == 0 {
		down = DefaultModelDownload
	}
	return &modelMeter{run: run, capUp: up, capDown: down, event: event}
}

// over names the cap the launch has passed ("" = none).
func (m *modelMeter) over() string {
	switch {
	case m.capUp > 0 && m.up.Load() > m.capUp:
		return "model_upload"
	case m.capDown > 0 && m.down.Load() > m.capDown:
		return "model_download"
	}
	return ""
}

// countingBody counts what is read of a request body.
type countingBody struct {
	io.ReadCloser
	n *atomic.Int64
	c *atomic.Int64
}

func (b countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n.Add(int64(n))
	b.c.Add(int64(n))
	return n, err
}

// countingWriter counts a response's bytes and status; it flushes through.
type countingWriter struct {
	http.ResponseWriter
	n      int64
	total  *atomic.Int64
	status int
}

func (w *countingWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *countingWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.n += int64(n)
	w.total.Add(int64(n))
	return n, err
}

// Unwrap lets http.ResponseController reach the connection's writer.
func (w *countingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *countingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// requestPath is a request's path for the audit: no query (a key may ride
// there), bounded.
func requestPath(r *http.Request) string {
	p := r.URL.EscapedPath()
	if len(p) > 200 {
		p = p[:200] + "…"
	}
	return p
}

// gatewayHandler serves one plan: the launch's token in, the operator's
// credential out, to exactly the configured upstream. meter (nil: none)
// audits and caps the launch's model traffic.
func gatewayHandler(g gatewayPlan, onRefuse func(string), meter *modelMeter) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(g.upstream)
			pr.Out.Host = g.upstream.Host
			for _, h := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key", "Proxy-Authorization"} {
				pr.Out.Header.Del(h)
			}
			if q := pr.Out.URL.Query(); q.Has("key") {
				q.Del("key")
				pr.Out.URL.RawQuery = q.Encode()
			}
			switch g.header {
			case "x-api-key":
				pr.Out.Header.Set("X-Api-Key", g.key)
			case "bearer":
				pr.Out.Header.Set("Authorization", "Bearer "+g.key)
			case "x-goog-api-key":
				pr.Out.Header.Set("X-Goog-Api-Key", g.key)
			}
		},
		FlushInterval: -1, // streamed responses stream
		ErrorLog:      nil,
	}
	// The upstream is conductor's own connection: never through a proxy the
	// environment names.
	rp.Transport = &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 30 * time.Second,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        8,
		IdleConnTimeout:     90 * time.Second,
	}
	want := []byte(g.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(presentedToken(r)), want) != 1 {
			if onRefuse != nil {
				onRefuse(r.Method + " " + r.URL.Path)
			}
			http.Error(w, "conductor model gateway: this launch's token is required", http.StatusUnauthorized)
			return
		}
		if meter == nil {
			rp.ServeHTTP(w, r)
			return
		}
		if which := meter.over(); which != "" {
			meter.capped.Do(func() {
				if meter.event != nil {
					meter.event("limit", map[string]any{"run": meter.run, "kind": which, "action": "refuse",
						"reason": fmt.Sprintf("model gateway: the launch passed sandbox.egress_limits.%s (sent %d, received %d bytes)", which, meter.up.Load(), meter.down.Load())})
				}
			})
			http.Error(w, "conductor model gateway: this launch's model traffic cap is reached (sandbox.egress_limits."+which+")", http.StatusTooManyRequests)
			return
		}
		start := time.Now()
		var up atomic.Int64
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = countingBody{ReadCloser: r.Body, n: &up, c: &meter.up}
		}
		cw := &countingWriter{ResponseWriter: w, total: &meter.down}
		rp.ServeHTTP(cw, r)
		if meter.event != nil {
			meter.event("model_request", map[string]any{"run": meter.run, "upstream": g.upstream.Host, "method": r.Method,
				"path": requestPath(r), "status": cw.status, "up": up.Load(), "down": cw.n, "duration_ms": time.Since(start).Milliseconds()})
		}
	})
}

// presentedToken is the credential a request carries, in any header a model
// client sends one in.
func presentedToken(r *http.Request) string {
	if v := r.Header.Get("X-Api-Key"); v != "" {
		return v
	}
	if v := r.Header.Get("Authorization"); v != "" {
		if t, ok := strings.CutPrefix(v, "Bearer "); ok {
			return t
		}
		return ""
	}
	for _, h := range []string{"X-Goog-Api-Key", "Api-Key"} {
		if v := r.Header.Get(h); v != "" {
			return v
		}
	}
	return r.URL.Query().Get("key")
}

// gatewayServer serves a plan on a listener until closed.
type gatewayServer struct {
	srv  *http.Server
	once sync.Once
	sock string
}

func serveGateway(ln net.Listener, g gatewayPlan, onRefuse func(string), meter *modelMeter) *gatewayServer {
	s := &gatewayServer{srv: &http.Server{Handler: gatewayHandler(g, onRefuse, meter), ReadHeaderTimeout: 30 * time.Second}}
	go func() { _ = s.srv.Serve(ln) }()
	return s
}

func (s *gatewayServer) Close() {
	s.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if s.srv.Shutdown(ctx) != nil {
			_ = s.srv.Close()
		}
		if s.sock != "" {
			_ = os.Remove(s.sock)
		}
	})
}

// listenGatewayUnix listens on a private unix socket (Linux: the forwarder
// relays the in-sandbox address to it).
func listenGatewayUnix() (net.Listener, string, func(), error) {
	dir, err := SocketDir("cgw")
	if err != nil {
		return nil, "", nil, err
	}
	sock := filepath.Join(dir, "g.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, "", nil, err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		_ = os.RemoveAll(dir)
		return nil, "", nil, fmt.Errorf("gateway socket: %w", err)
	}
	return ln, sock, func() { _ = os.RemoveAll(dir) }, nil
}

// gatewayListenAddr picks the in-sandbox address for the i-th gateway: one
// no relay or exposed server of the launch uses.
func gatewayListenAddr(i int, taken []string) string {
	used := map[string]bool{}
	for _, t := range taken {
		used[t] = true
	}
	for port := 47100 + i; ; port++ {
		a := net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
		if !used[a] {
			return a
		}
	}
}

// agentModelEnv rewrites the agent's environment for its gateways: each
// route's base URL is the gateway, its token the only credential.
func agentModelEnv(env []string, plans []gatewayPlan, urls []string) []string {
	drop := map[string]bool{}
	for _, g := range plans {
		drop[g.route.baseEnv] = true
		drop[g.route.agentKey] = true
		for _, k := range g.route.keys {
			drop[k.env] = true
		}
	}
	out := make([]string, 0, len(env)+2*len(plans))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !drop[k] {
			out = append(out, kv)
		}
	}
	for i, g := range plans {
		out = append(out, g.route.baseEnv+"="+urls[i], g.route.agentKey+"="+g.token)
	}
	return out
}

// errNoGatewayRelay: a Linux launch whose gateway cannot be relayed in.
var errNoGatewayRelay = errors.New("agentsandbox: the model gateway needs the in-sandbox forwarder")

// fileKeys are model keys the operator keeps in a tool's own files rather
// than the environment: claude-code's settings.json env block, codex's
// auth.json API key. Read from the operator's real home, host side.
func fileKeys(tool, realHome string) map[string]string {
	out := map[string]string{}
	if realHome == "" {
		return out
	}
	switch tool {
	case "claude-code", "pi":
		for k, v := range claudeSettingsEnv(filepath.Join(realHome, ".claude", "settings.json")) {
			out[k] = v
		}
	case "codex":
		var a struct {
			Key string `json:"OPENAI_API_KEY"`
		}
		if b, err := os.ReadFile(filepath.Join(realHome, ".codex", "auth.json")); err == nil && json.Unmarshal(b, &a) == nil && a.Key != "" {
			out["OPENAI_API_KEY"] = a.Key
		}
	}
	return out
}

// credentialKey reports a login-file field that is a long-lived
// credential: a refresh token, or an API key kept beside the login.
func credentialKey(k string) bool {
	l := strings.ToLower(k)
	return strings.Contains(l, "refresh") || strings.Contains(l, "api_key") || strings.Contains(l, "apikey")
}

// accessOnlyCopy is a login file without its long-lived credentials: every
// refresh token and API key removed, at any depth, so what the agent holds
// is the short-lived access token. A file that is not JSON is not copied
// (nil).
func accessOnlyCopy(b []byte) ([]byte, error) {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil, nil
	}
	var strip func(any) any
	strip = func(x any) any {
		switch t := x.(type) {
		case map[string]any:
			for k, c := range t {
				if credentialKey(k) {
					delete(t, k)
					continue
				}
				t[k] = strip(c)
			}
		case []any:
			for i := range t {
				t[i] = strip(t[i])
			}
		}
		return x
	}
	return json.Marshal(strip(v))
}

// homeDir makes dir (inside home) a real directory chain: no component the
// agent could have replaced with a symlink pointing out of its home.
func homeDir(home, dir string) error {
	rel, err := filepath.Rel(home, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s is outside %s", dir, home)
	}
	cur := home
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		switch {
		case os.IsNotExist(err):
			if err := os.Mkdir(cur, 0o700); err != nil {
				return err
			}
		case err != nil:
			return err
		case !fi.IsDir():
			return fmt.Errorf("%s is not a directory", cur)
		}
	}
	return nil
}

// writeInHome replaces p (inside home) by rename: never written through a
// symlink, never into a directory outside home.
func writeInHome(home, p string, b []byte) error {
	if err := homeDir(home, filepath.Dir(p)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".conductor-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// readInHome reads p without following a symlink (nil: absent, or not a
// plain file).
func readInHome(p string) []byte {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(f, 16<<20))
	return b
}

// scrubClaudeHome removes, from a claude-code identity home, what would
// route the agent around its gateway or hand it a key: the settings.json
// env entries for the gatewayed routes (claude-code applies them over its
// environment), an apiKeyHelper, and a stored primaryApiKey.
func scrubClaudeHome(home string, plans []gatewayPlan) error {
	drop := map[string]bool{"CLAUDE_CODE_OAUTH_TOKEN": true}
	for _, g := range plans {
		drop[g.route.baseEnv], drop[g.route.agentKey] = true, true
		for _, k := range g.route.keys {
			drop[k.env] = true
		}
	}
	sp := filepath.Join(home, ".claude", "settings.json")
	if b := readInHome(sp); b != nil {
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) == nil {
			changed := false
			if _, ok := m["apiKeyHelper"]; ok {
				delete(m, "apiKeyHelper")
				changed = true
			}
			var env map[string]json.RawMessage
			if raw, ok := m["env"]; ok && json.Unmarshal(raw, &env) == nil {
				for k := range env {
					if drop[k] {
						delete(env, k)
						changed = true
					}
				}
				m["env"], _ = json.Marshal(env)
			}
			if changed {
				out, _ := json.Marshal(m)
				if err := writeInHome(home, sp, out); err != nil {
					return err
				}
			}
		}
	}
	cp := filepath.Join(home, ".claude.json")
	if b := readInHome(cp); b != nil {
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) == nil {
			if _, ok := m["primaryApiKey"]; ok {
				delete(m, "primaryApiKey")
				out, _ := json.Marshal(m)
				return writeInHome(home, cp, out)
			}
		}
	}
	return nil
}

// modelCreds is how one launch's model credentials are arranged.
type modelCreds struct {
	plans []gatewayPlan
	// share: bind the operator's login files read-only (login-share).
	share bool
	// noLogins: every route is gatewayed, so no login file is needed.
	noLogins bool
}

// planModelCredentials decides p's model credentials and narrows p: a
// gatewayed loopback router is no longer relayed or allowed directly, and
// when every route is gatewayed the provider hosts leave the allowlist.
func planModelCredentials(p *Policy, d Deps) (*modelCreds, error) {
	mc := &modelCreds{}
	switch p.ModelCredential {
	case CredLoginShare:
		mc.share = true
		return mc, nil
	case CredLoginCopy:
		return mc, nil
	}
	plans, left, err := planGateways(p.Tool, d.Inherited, fileKeys(p.Tool, d.RealHome))
	if err != nil || len(plans) == 0 {
		return mc, err
	}
	mc.plans = plans
	mc.noLogins = len(left) == 0
	routed := map[string]bool{}
	for _, g := range plans {
		if g.loopback != "" {
			routed[g.loopback] = true
		}
	}
	var lb []string
	for _, l := range p.Loopback {
		if !routed[l] {
			lb = append(lb, l)
		}
	}
	p.Loopback = lb
	if mc.noLogins {
		model := map[string]bool{}
		for _, h := range p.ModelHosts {
			model[h] = true
		}
		var eg []string
		for _, h := range p.Egress {
			if !model[h] {
				eg = append(eg, h)
			}
		}
		p.Egress = eg
	}
	if p.Tool == "claude-code" || p.Tool == "pi" {
		if err := scrubClaudeHome(p.Home, plans); err != nil {
			return nil, fmt.Errorf("agentsandbox: scrub the identity home's model settings: %w", err)
		}
	}
	return mc, nil
}

// placeLogins puts p's login files in its identity home: shared read-only
// (binds returned), an access-token-only copy, or — every route gatewayed —
// none at all.
func (mc *modelCreds) placeLogins(p Policy) ([]sandbox.BindMount, error) {
	var binds []sandbox.BindMount
	for _, l := range p.Logins {
		if mc.share {
			// Linux binds the operator's file over the placeholder; on macOS
			// the identity home links to it, and seatbeltBinds allows its
			// source. Read-only: a refresh the agent makes stays in memory.
			binds = append(binds, sandbox.BindMount{Path: l.Path, Src: l.Src, RO: true})
			continue
		}
		var b []byte
		if !mc.noLogins {
			src, err := os.ReadFile(l.Src)
			if err == nil {
				b, err = accessOnlyCopy(src)
			}
			if err != nil {
				return nil, fmt.Errorf("agentsandbox: login copy of %s: %w", l.Src, err)
			}
		}
		if b == nil {
			if fi, err := os.Lstat(l.Path); err == nil && (fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0) {
				if err := os.Remove(l.Path); err != nil {
					return nil, err
				}
			}
			continue
		}
		if err := writeInHome(p.Home, l.Path, b); err != nil {
			return nil, fmt.Errorf("agentsandbox: login copy into %s: %w", l.Path, err)
		}
	}
	return binds, nil
}
