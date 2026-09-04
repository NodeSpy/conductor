package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
)

// opencodeController drives opencode over its native HTTP server (`opencode
// serve`) rather than ACP — the transport that exposes opencode's own model
// routing and cost accounting. A session is created against the conductor-
// provisioned worktree and prompted over HTTP; opencode keeps the session by id, so
// session_model is resumable.
//
// Acts-as-user identity and the worktree are supplied to the server process at
// launch (cwd + env), because per-request env can't cross the HTTP boundary — the
// dialer starts `opencode serve` in the worktree with conductor's identity env, or
// a test injects a server URL directly.
type opencodeController struct {
	name string
	host string // hosts: entry the server launches on over SSH ("" = local)
	prov Provisioner
	dial opencodeDialer // injectable; nil → spawn `opencode serve`
	hc   *http.Client
}

// opencodeDialer resolves a base URL for an opencode server rooted at cwd with env
// applied, plus a cleanup that stops any process it started.
type opencodeDialer func(ctx context.Context, cwd string, env []string) (baseURL string, cleanup func() error, err error)

// newOpencodeController builds an opencode native controller. With host: set,
// `opencode serve` launches on that box over SSH — still bound to the REMOTE
// 127.0.0.1 — and every HTTP request reaches it through an `ssh -W` stdio
// forward (HostDial), so no port is exposed on either machine.
func newOpencodeController(name string, cc config.ControllerConfig, prov Provisioner) *opencodeController {
	c := &opencodeController{
		name: name,
		host: cc.Host,
		prov: prov,
		hc:   &http.Client{Timeout: 0}, // no client timeout: an agent turn can run long
	}
	if cc.Host != "" {
		host := cc.Host
		c.hc = &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				if HostDial == nil {
					return nil, fmt.Errorf("opencode: host %q configured but no host dialer is wired", host)
				}
				return HostDial(ctx, host, addr)
			},
			// One ssh subprocess per connection; keep-alives would pin them.
			DisableKeepAlives: true,
		}}
	}
	return c
}

func (c *opencodeController) Name() string         { return c.name }
func (c *opencodeController) Model() SessionModel  { return ModelResumable }
func (c *opencodeController) Transport() Transport { return TransportNative }

// Initialize reports opencode's native capabilities. A session is resumable by id,
// runs in the conductor-provisioned worktree, accepts follow-up turns, and can be
// remote (the server may be off-box).
func (c *opencodeController) Initialize(context.Context) (Capabilities, error) {
	return Capabilities{
		SessionModel:       ModelResumable,
		Transport:          TransportNative,
		CheckoutPR:         true,
		InteractiveHandoff: false, // native HTTP has no permission-callback channel yet (M2)
		SendFollowup:       true,
		Remote:             true,
	}, nil
}

func (c *opencodeController) Runner() (Runner, error) {
	return newControllerRunner(c, c.prov, nil), nil
}

// NewSession starts (or connects to) an opencode server rooted at the worktree,
// creates a session, and runs its first prompt turn asynchronously.
func (c *opencodeController) NewSession(ctx context.Context, spec Spec, _ Handler) (Session, error) {
	env, err := dispatch.AgentEnv(spec.Request)
	if err != nil {
		return nil, fmt.Errorf("opencode: build env: %w", err)
	}
	prompt, err := dispatch.RenderPrompt(spec.Request)
	if err != nil {
		return nil, fmt.Errorf("opencode: render prompt: %w", err)
	}

	sctx, scancel := context.WithCancel(context.Background())
	baseURL, cleanup, err := c.connect(sctx, spec.Cwd, env)
	if err != nil {
		scancel()
		return nil, err
	}
	cl := &opencodeClient{baseURL: strings.TrimRight(baseURL, "/"), hc: c.hc}

	title := opencodeTitle(spec.Request)
	id, err := cl.createSession(sctx, spec.Cwd, title)
	if err != nil {
		cleanup()
		scancel()
		return nil, fmt.Errorf("opencode: create session: %w", err)
	}

	s := &opencodeSession{
		id:       id,
		cl:       cl,
		cleanup:  cleanup,
		cancel:   scancel,
		ctx:      sctx,
		provider: spec.Request.Profile.Provider,
		model:    spec.Request.Profile.Model,
	}
	s.startTurn(prompt)
	return s, nil
}

// ResumeSession re-binds an existing opencode session by id (resumable by id).
func (c *opencodeController) ResumeSession(ctx context.Context, id string, _ Handler) (Session, error) {
	sctx, scancel := context.WithCancel(context.Background())
	baseURL, cleanup, err := c.connect(sctx, "", nil)
	if err != nil {
		scancel()
		return nil, err
	}
	cl := &opencodeClient{baseURL: strings.TrimRight(baseURL, "/"), hc: c.hc}
	return &opencodeSession{id: id, cl: cl, cleanup: cleanup, cancel: scancel, ctx: sctx}, nil
}

func (c *opencodeController) connect(ctx context.Context, cwd string, env []string) (string, func() error, error) {
	if c.dial != nil {
		return c.dial(ctx, cwd, env)
	}
	return spawnOpencode(ctx, c.host, cwd, env)
}

// spawnOpencode starts `opencode serve` in the worktree with the identity env and
// returns the URL it advertises on stdout. The process lifetime is owned by the
// returned cleanup (session-scoped). With host set, the launch wraps over SSH
// (prepareLaunch): the server binds the REMOTE 127.0.0.1, its stdout — with
// the advertised URL — streams back over the ssh channel, and the controller's
// HTTP client reaches it via ssh -W. A locally-provisioned worktree path is
// not meaningful on the remote box, so remote sessions want checkout: none or
// a remote-existing directory.
func spawnOpencode(_ context.Context, host, cwd string, env []string) (string, func() error, error) {
	argv := []string{"opencode", "serve", "--hostname", "127.0.0.1", "--port", "0"}
	argv, localDir, remote, err := prepareLaunch(host, cwd, env, argv)
	if err != nil {
		return "", nil, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if localDir != "" {
		cmd.Dir = localDir
	}
	if !remote {
		cmd.Env = append(os.Environ(), env...)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("opencode: start serve: %w", err)
	}
	cleanup := func() error {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return nil
	}
	url, err := scanOpencodeURL(stdout)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return url, cleanup, nil
}

// scanOpencodeURL reads server stdout until it prints the http URL it's listening
// on, returning that URL. `opencode serve` announces e.g.
// "opencode server listening on http://127.0.0.1:39481".
func scanOpencodeURL(r io.Reader) (string, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 512)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if i := strings.Index(string(buf), "http://"); i >= 0 {
				rest := string(buf[i:])
				if end := strings.IndexAny(rest, " \r\n"); end >= 0 {
					return rest[:end], nil
				}
			}
		}
		if err != nil {
			return "", fmt.Errorf("opencode: never advertised a listen URL: %w", err)
		}
	}
}

// opencodeTitle encodes the PR identity in the session title, matching the
// convention used across controllers.
func opencodeTitle(req dispatch.Request) string {
	return fmt.Sprintf("conductor: %s %s", req.Trigger.Target.Repo, req.Trigger.Kind)
}

// ---- HTTP client --------------------------------------------------------------

// opencodeClient is a thin client for the opencode server's session/message API.
type opencodeClient struct {
	baseURL string
	hc      *http.Client
}

// createSession opens a session (POST /session), returning its id. directory roots
// the session at the worktree when the server supports it; title carries the PR
// identity.
func (c *opencodeClient) createSession(ctx context.Context, directory, title string) (string, error) {
	body := map[string]any{}
	if directory != "" {
		body["directory"] = directory
	}
	if title != "" {
		body["title"] = title
	}
	var res map[string]any
	if err := c.do(ctx, http.MethodPost, "/session", body, &res); err != nil {
		return "", err
	}
	for _, k := range []string{"id", "sessionID", "sessionId"} {
		if v, ok := res[k].(string); ok && v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("opencode: create session returned no id (%v)", res)
}

// prompt sends one prompt turn (POST /session/{id}/message) and returns the
// assistant's assembled text. provider/model route the turn when set.
func (c *opencodeClient) prompt(ctx context.Context, sessionID, text, provider, model string) (string, error) {
	body := map[string]any{
		"parts": []map[string]any{{"type": "text", "text": text}},
	}
	if provider != "" {
		body["providerID"] = provider
	}
	if model != "" {
		body["modelID"] = model
	}
	var res opencodeMessage
	if err := c.do(ctx, http.MethodPost, "/session/"+sessionID+"/message", body, &res); err != nil {
		return "", err
	}
	return res.text(), nil
}

// opencodeMessage is the subset of an opencode assistant message we read: the text
// parts, assembled into the turn's output.
type opencodeMessage struct {
	Parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"parts"`
}

func (m opencodeMessage) text() string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func (c *opencodeClient) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("opencode: %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// ---- session ------------------------------------------------------------------

// opencodeSession is one live opencode session driven over HTTP.
type opencodeSession struct {
	id       string
	cl       *opencodeClient
	cleanup  func() error
	cancel   context.CancelFunc
	ctx      context.Context
	provider string
	model    string

	mu   sync.Mutex
	done chan struct{}
}

func (s *opencodeSession) ID() string { return s.id }

// Prompt runs a follow-up turn on the resumable session.
func (s *opencodeSession) Prompt(_ context.Context, msg Message) (<-chan Update, error) {
	return s.startTurn(msg.Text), nil
}

func (s *opencodeSession) startTurn(text string) <-chan Update {
	ch := make(chan Update, 4)
	done := make(chan struct{})
	s.mu.Lock()
	s.done = done
	s.mu.Unlock()

	go func() {
		defer close(done)
		defer close(ch)
		sendUpdate(ch, Update{Kind: UpdateStarted, AgentID: s.id})
		out, err := s.cl.prompt(s.ctx, s.id, text, s.provider, s.model)
		sendUpdate(ch, Update{Kind: UpdateDone, AgentID: s.id, Output: out, Err: err})
	}()
	return ch
}

func (s *opencodeSession) Wait(ctx context.Context, timeout time.Duration) {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done == nil {
		return
	}
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		select {
		case <-done:
		case <-ctx.Done():
		case <-t.C:
		}
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Cancel aborts the in-flight turn (POST /session/{id}/abort), best-effort.
func (s *opencodeSession) Cancel(ctx context.Context) error {
	return s.cl.do(ctx, http.MethodPost, "/session/"+s.id+"/abort", map[string]any{}, nil)
}

func (s *opencodeSession) Close(context.Context) error {
	s.cancel()
	if s.cleanup != nil {
		return s.cleanup()
	}
	return nil
}
