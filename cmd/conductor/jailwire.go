package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/hostcmd"
	"github.com/NodeSpy/conductor/internal/jail"
	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/targets"
)

// jailWiring is what wireJail needs from the boot path.
type jailWiring struct {
	cfg      *config.Config
	stateDir string
	cfgDir   string
	audit    func(map[string]any)
	events   *flow.EventHub
	egress   *sandbox.ProxyManager
	logf     func(string, ...any)
}

// wireJail builds the agent-jail manager (#154) and hands it to the
// controller package: every cli/acp launch on this box then runs in the
// workspace jail unless its isolation says otherwise. Every boundary crossing
// — host command, push, fetch, signature, tool call, egress — is an audit
// row and a live `conductor watch` event attributed to the dispatch.
func wireJail(w jailWiring) *jail.Manager {
	home, _ := os.UserHomeDir()
	emit := func(e jail.Event) {
		row := map[string]any{"event": e.Type, "status": e.Status, "dispatch": e.Dispatch, "label": e.Label,
			"repo": e.Repo, "number": e.Number, "step": e.Step, "detail": e.Detail}
		if e.Reason != "" {
			row["reason"] = e.Reason
		}
		for k, v := range e.Fields {
			row[k] = v
		}
		w.audit(row)
		if w.events != nil {
			run := e.Dispatch
			if i := strings.LastIndexByte(run, ':'); i > 0 {
				run = run[:i]
			}
			detail := e.Detail
			if e.Reason != "" {
				detail += " — " + e.Reason
			}
			if d, ok := e.Fields["discarded"].([]string); ok && len(d) > 0 {
				detail += fmt.Sprintf(" (%d write(s) discarded: %s)", len(d), strings.Join(d[:min(3, len(d))], ", "))
			}
			w.events.Publish(flow.RunEvent{Type: e.Type, Run: run, RunID: e.Label, Repo: e.Repo, Number: e.Number,
				Step: e.Step, Status: e.Status, Detail: detail})
		}
	}
	target := func(d *jail.Dispatch) targets.Target {
		return targets.Target{Repo: d.Repo, Number: d.Number, IsPR: d.IsPR, HeadBranch: d.HeadBranch}
	}
	policy := func(d *jail.Dispatch) targets.WritePolicy {
		p := targets.WritePolicy{ReadOnly: d.ReadOnly}
		if wp := d.Writes; wp != nil {
			p.CreatePR, p.CreateIssue, p.OtherTargets, p.Branches, p.Merge = wp.CreatePR, wp.CreateIssue, wp.OtherTargets, wp.Branches, wp.Merge
		}
		return p
	}
	m := &jail.Manager{
		Root:      filepath.Join(w.stateDir, "jails"),
		SelfExe:   os.Executable,
		LookPath:  exec.LookPath,
		Home:      home,
		Sensitive: []string{w.stateDir, w.cfgDir},
		Sockets:   []string{filepath.Join(w.stateDir, "memory.sock")},
		Emit:      emit,
		CheckWrite: func(d *jail.Dispatch, wr hostcmd.Write) string {
			if d.Repo == "" {
				return "target: this launch has no dispatch target — writes are refused"
			}
			// A dead target trumps everything else: say so, whatever the write.
			if r := liveClosed(d); r != "" {
				return r
			}
			return targets.Default.CheckWrite(target(d), policy(d), wr.Kind, wr.Repo, wr.Number)
		},
		CheckPush: func(d *jail.Dispatch, branch string, force, del bool) string {
			if d.Repo == "" {
				return "target: this launch has no dispatch target — pushes are refused"
			}
			if r := liveClosed(d); r != "" {
				return r
			}
			return targets.Default.CheckPush(target(d), policy(d), d.Repo, branch, force, del)
		},
		ThreadTarget:  threadTarget,
		HostEgress:    w.egress.UnixEndpointLabeled,
		HostEgressTCP: w.egress.EndpointLabeled,
	}
	// A stale jail dir from a crashed daemon holds nothing anyone needs.
	if ents, err := os.ReadDir(m.Root); err == nil {
		for _, e := range ents {
			_ = os.RemoveAll(filepath.Join(m.Root, e.Name()))
		}
	}
	w.egress.OnVerdict = m.NetVerdict
	controller.JailManager = m
	controller.GlobalIsolation = w.cfg.Isolation
	dispatch.GlobalIsolation = w.cfg.Isolation
	controller.EgressProxyUnixLabeled = w.egress.UnixEndpointLabeled
	controller.EgressProxyForLabeled = w.egress.EndpointLabeled
	controller.JailDegraded = func(dispatchID, label, reason string) {
		emit(jail.Event{Type: "jail", Status: "degraded", Dispatch: dispatchID, Label: label,
			Reason: "the default workspace jail is unavailable (" + reason + ") — this agent ran WITHOUT confinement"})
	}
	if why := controller.ProbeJail(); why != "" {
		w.logf("WARNING: agent workspace jail unavailable on this box: %s — cli/acp agents run unconfined unless an explicit isolation: block requires it", why)
	}
	return m
}

// liveClosed asks GitHub whether the dispatch's PR is still open — the
// write-time backstop for a close conductor has not heard about yet (a
// missed webhook). A failed lookup does not block: the closed-target
// registry (fed by the close event) is the primary signal.
func liveClosed(d *jail.Dispatch) string {
	if !d.IsPR || d.Number == 0 || d.UserToken == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var pr struct {
		State  string `json:"state"`
		Merged bool   `json:"merged"`
	}
	if err := githubGet(ctx, d.UserToken, fmt.Sprintf("/repos/%s/pulls/%d", d.Repo, d.Number), &pr); err != nil {
		return ""
	}
	if pr.State == "closed" {
		targets.Default.MarkClosed(d.Repo, d.Number, pr.Merged)
		outcome := "closed"
		if pr.Merged {
			outcome = "merged"
		}
		return fmt.Sprintf("target: %s#%d is %s — writes refused", d.Repo, d.Number, outcome)
	}
	return ""
}

// threadTarget resolves a review-thread node id to the PR it belongs to, so
// resolving a thread is bound to the dispatch's own PR.
func threadTarget(ctx context.Context, d *jail.Dispatch, id string) (string, int, error) {
	if d.UserToken == "" {
		return "", 0, fmt.Errorf("no GitHub token to resolve the thread")
	}
	q := map[string]any{
		"query":     `query($id: ID!) { node(id: $id) { ... on PullRequestReviewThread { pullRequest { number repository { nameWithOwner } } } } }`,
		"variables": map[string]any{"id": id},
	}
	var out struct {
		Data struct {
			Node struct {
				PullRequest struct {
					Number     int `json:"number"`
					Repository struct {
						NameWithOwner string `json:"nameWithOwner"`
					} `json:"repository"`
				} `json:"pullRequest"`
			} `json:"node"`
		} `json:"data"`
	}
	if err := githubPost(ctx, d.UserToken, "/graphql", q, &out); err != nil {
		return "", 0, err
	}
	pr := out.Data.Node.PullRequest
	if pr.Number == 0 {
		return "", 0, fmt.Errorf("%s is not a review thread", id)
	}
	return pr.Repository.NameWithOwner, pr.Number, nil
}

func githubAPIBase() string {
	if v := os.Getenv("PC_GITHUB_API_BASE"); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	return "https://api.github.com"
}

func githubGet(ctx context.Context, tok, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIBase()+path, nil)
	if err != nil {
		return err
	}
	return githubDo(req, tok, into)
}

func githubPost(ctx context.Context, tok, path string, body, into any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubAPIBase()+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return githubDo(req, tok, into)
}

func githubDo(req *http.Request, tok string, into any) error {
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("github %s: %s", req.URL.Path, resp.Status)
	}
	return json.Unmarshal(b, into)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
