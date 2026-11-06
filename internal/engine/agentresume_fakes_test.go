package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/NodeSpy/conductor/internal/acp"
)

// The restart e2e's stand-ins for an agent daemon and an agent, run as
// subprocesses: the test binary re-executed with AGENTRESUME_FAKE naming the
// role, its state kept under AGENTRESUME_STATE so it outlives a daemon life.
//
//   - paseo: the `paseo` CLI over a persistent daemon. A fresh `run` leaves
//     uncommitted work in its workspace and keeps working until killed — the
//     agent stays in the daemon, as a real paseo agent does when conductor
//     stops. `send` is the agent's next turn.
//   - acp: an ACP agent that persists its sessions and loads them back
//     (session/load). A fresh session's turn leaves uncommitted work and runs
//     until cancelled.
//
// Every launch, load and turn is appended to $AGENTRESUME_STATE/LOG; a
// LOG.fail-resume file makes the resume fail.

func init() {
	switch os.Getenv("AGENTRESUME_FAKE") {
	case "paseo":
		fakePaseo(os.Args[1:])
		os.Exit(0)
	case "acp":
		fakeACPAgentMain()
		os.Exit(0)
	}
}

func fakeState(name string) string { return filepath.Join(os.Getenv("AGENTRESUME_STATE"), name) }

func fakeLog(format string, args ...any) {
	f, err := os.OpenFile(fakeState("LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, format+"\n", args...)
}

func failResume() bool {
	_, err := os.Stat(fakeState("LOG.fail-resume"))
	return err == nil
}

func wip(cwd string) string {
	b, _ := os.ReadFile(filepath.Join(cwd, "wip.txt"))
	return string(b)
}

// ---- paseo ------------------------------------------------------------------

type fakePaseoAgent struct {
	ID     string            `json:"id"`
	Cwd    string            `json:"cwd"`
	Status string            `json:"status"`
	Labels map[string]string `json:"labels"`
}

type fakePaseoWorkspace struct {
	WorkspaceID string `json:"workspaceId"`
	Cwd         string `json:"cwd"`
	Name        string `json:"name"`
	Isolation   string `json:"isolation"`
}

type fakePaseoDaemon struct {
	Seq        int                            `json:"seq"`
	Agents     map[string]*fakePaseoAgent     `json:"agents"`
	Workspaces map[string]*fakePaseoWorkspace `json:"workspaces"`
}

// withPaseoState runs fn on the daemon's state under a lock, then saves it.
func withPaseoState(fn func(s *fakePaseoDaemon)) {
	lock, err := os.OpenFile(fakeState("paseo.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		panic(err)
	}
	defer lock.Close()
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
	s := &fakePaseoDaemon{Agents: map[string]*fakePaseoAgent{}, Workspaces: map[string]*fakePaseoWorkspace{}}
	if b, err := os.ReadFile(fakeState("paseo.json")); err == nil {
		_ = json.Unmarshal(b, s)
	}
	fn(s)
	b, _ := json.Marshal(s)
	if err := os.WriteFile(fakeState("paseo.json"), b, 0o600); err != nil {
		panic(err)
	}
}

func flagValues(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func printJSON(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func fakePaseo(args []string) {
	if len(args) == 0 {
		os.Exit(2)
	}
	switch args[0] {
	case "workspace":
		switch args[1] {
		case "create":
			var ws *fakePaseoWorkspace
			withPaseoState(func(s *fakePaseoDaemon) {
				s.Seq++
				ws = &fakePaseoWorkspace{WorkspaceID: fmt.Sprintf("ws-%d", s.Seq), Cwd: flagValues(args, "--path")[0],
					Name: flagValues(args, "--title")[0], Isolation: "local"}
				s.Workspaces[ws.WorkspaceID] = ws
			})
			printJSON(ws)
		case "ls":
			var out []*fakePaseoWorkspace
			withPaseoState(func(s *fakePaseoDaemon) {
				for _, w := range s.Workspaces {
					out = append(out, w)
				}
			})
			printJSON(out)
		case "archive":
			withPaseoState(func(s *fakePaseoDaemon) {
				if w := s.Workspaces[args[2]]; w != nil {
					for id, a := range s.Agents {
						if a.Cwd == w.Cwd {
							delete(s.Agents, id)
						}
					}
				}
				delete(s.Workspaces, args[2])
			})
			fakeLog("workspace archived %s", args[2])
		}
	case "run":
		prompt := args[1]
		labels := map[string]string{}
		for _, l := range flagValues(args, "--label") {
			k, v, _ := strings.Cut(l, "=")
			labels[k] = v
		}
		var a *fakePaseoAgent
		withPaseoState(func(s *fakePaseoDaemon) {
			s.Seq++
			a = &fakePaseoAgent{ID: fmt.Sprintf("agent-%d", s.Seq), Status: "running", Labels: labels}
			if ws := flagValues(args, "--workspace"); len(ws) == 1 && s.Workspaces[ws[0]] != nil {
				a.Cwd = s.Workspaces[ws[0]].Cwd
			}
			s.Agents[a.ID] = a
		})
		token := labels["conductor-session"]
		if strings.Contains(prompt, "earlier agent was working") {
			fakeLog("fresh-in-kept label=%s cwd=%s wip=%s", token, a.Cwd, wip(a.Cwd))
			printJSON(map[string]any{"agentId": a.ID, "output": "done"})
			return
		}
		_ = os.WriteFile(filepath.Join(a.Cwd, "wip.txt"), []byte("half done"), 0o600)
		fakeLog("fresh label=%s cwd=%s", token, a.Cwd)
		time.Sleep(time.Minute) // the turn runs on; conductor stops while it does
	case "ls":
		want := map[string]string{}
		for _, l := range flagValues(args, "--label") {
			k, v, _ := strings.Cut(l, "=")
			want[k] = v
		}
		out := []*fakePaseoAgent{}
		withPaseoState(func(s *fakePaseoDaemon) {
			for _, a := range s.Agents {
				match := true
				for k, v := range want {
					match = match && a.Labels[k] == v
				}
				if match {
					out = append(out, a)
				}
			}
		})
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		printJSON(out)
	case "wait":
		fakeLog("wait %s", args[1])
	case "send":
		var a *fakePaseoAgent
		withPaseoState(func(s *fakePaseoDaemon) {
			if a = s.Agents[args[1]]; a != nil {
				a.Status = "idle"
			}
		})
		if a == nil || failResume() {
			fmt.Fprintf(os.Stderr, "agent %s not found\n", args[1])
			os.Exit(1)
		}
		fakeLog("resume agent=%s label=%s cwd=%s wip=%s", a.ID, a.Labels["conductor-session"], a.Cwd, wip(a.Cwd))
		_ = os.WriteFile(fakeState("LOG.prompt"), []byte(args[2]), 0o600)
		printJSON(map[string]any{"agentId": a.ID, "output": "done"})
	case "archive":
		withPaseoState(func(s *fakePaseoDaemon) { delete(s.Agents, args[1]) })
		fakeLog("agent archived %s", args[1])
	default:
		printJSON(map[string]any{})
	}
}

// ---- acp --------------------------------------------------------------------

type fakeACP struct {
	conn  *acp.Conn
	ready chan struct{} // closed once conn is set

	mu        sync.Mutex
	cwd       map[string]string // session → cwd
	cancelled chan struct{}
}

// sessions are persisted, as an agent that supports session/load keeps them.
func (a *fakeACP) saveSession(id, cwd string) {
	_ = os.WriteFile(fakeState("acp-"+id), []byte(cwd), 0o600)
}

func (a *fakeACP) HandleRequest(ctx context.Context, method string, params json.RawMessage) (any, *acp.RPCError) {
	switch method {
	case acp.MethodInitialize:
		return acp.InitializeResult{ProtocolVersion: 1, AgentCapabilities: acp.AgentCapabilities{LoadSession: true}}, nil
	case acp.MethodNewSession:
		var p acp.NewSessionParams
		_ = json.Unmarshal(params, &p)
		id := fmt.Sprintf("acp-sess-%d", time.Now().UnixNano())
		a.mu.Lock()
		a.cwd[id] = p.Cwd
		a.mu.Unlock()
		a.saveSession(id, p.Cwd)
		return acp.NewSessionResult{SessionID: id}, nil
	case acp.MethodLoadSession:
		var p acp.LoadSessionParams
		_ = json.Unmarshal(params, &p)
		if _, err := os.Stat(fakeState("acp-" + p.SessionID)); err != nil || failResume() {
			fakeLog("load-failed sid=%s", p.SessionID)
			return nil, acp.NewRPCError(acp.CodeInternalError, "no such session "+p.SessionID)
		}
		a.mu.Lock()
		a.cwd[p.SessionID] = p.Cwd
		a.mu.Unlock()
		fakeLog("load sid=%s cwd=%s", p.SessionID, p.Cwd)
		// The replay of the session's history comes ahead of the reply.
		a.say(ctx, p.SessionID, "earlier history ")
		return struct{}{}, nil
	case acp.MethodPrompt:
		var p acp.PromptParams
		_ = json.Unmarshal(params, &p)
		text := ""
		if len(p.Prompt) > 0 {
			text = p.Prompt[0].Text
		}
		a.mu.Lock()
		cwd, known := a.cwd[p.SessionID]
		a.mu.Unlock()
		switch {
		case strings.Contains(text, "earlier agent was working"):
			fakeLog("fresh-in-kept sid=%s cwd=%s wip=%s", p.SessionID, cwd, wip(cwd))
		case strings.HasPrefix(text, "Conductor restarted"):
			fakeLog("resume sid=%s cwd=%s wip=%s", p.SessionID, cwd, wip(cwd))
			_ = os.WriteFile(fakeState("LOG.prompt"), []byte(text), 0o600)
		case known:
			_ = os.WriteFile(filepath.Join(cwd, "wip.txt"), []byte("half done"), 0o600)
			fakeLog("fresh sid=%s cwd=%s", p.SessionID, cwd)
			<-a.cancelled // the turn runs on until conductor stops it
			return acp.PromptResult{StopReason: acp.StopReasonCancelled}, nil
		}
		a.say(ctx, p.SessionID, "done")
		return acp.PromptResult{StopReason: acp.StopReasonEndTurn}, nil
	}
	return nil, acp.NewRPCError(acp.CodeMethodNotFound, method)
}

func (a *fakeACP) say(ctx context.Context, sid, text string) {
	<-a.ready
	_ = a.conn.Notify(ctx, acp.MethodSessionUpdate, acp.SessionNotification{SessionID: sid,
		Update: acp.SessionUpdate{Kind: acp.UpdateAgentMessageChunk, MessageChunk: &acp.MessageChunk{Content: acp.TextBlock(text)}}})
}

func (a *fakeACP) HandleNotification(_ context.Context, method string, _ json.RawMessage) {
	if method == acp.MethodSessionCancel {
		a.mu.Lock()
		defer a.mu.Unlock()
		select {
		case <-a.cancelled:
		default:
			close(a.cancelled)
		}
	}
}

func fakeACPAgentMain() {
	a := &fakeACP{cwd: map[string]string{}, cancelled: make(chan struct{}), ready: make(chan struct{})}
	a.conn = acp.NewConn(os.Stdin, os.Stdout, a)
	close(a.ready)
	<-a.conn.Done()
}
