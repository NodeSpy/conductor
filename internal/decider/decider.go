// Package decider drives DECISION runtimes: runtime plugins that answer a
// decision protocol natively (Decl.Protocols — today system_one/v1, as Jev
// does) and serve only decide: steps, never an agent.
//
// A decision runtime is reached over the ordinary plugin.invoke with two
// verbs: `decide` answers one request, `models` lists the roster fleets
// resolve against. The runtime is third-party code talking to a third-party
// API, so nothing it returns is trusted: every answer is validated against
// the questions that were asked (systemone.ValidateAnswers) before a
// workflow can read it.
package decider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/acp"
	"github.com/NodeSpy/conductor/internal/models"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/systemone"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// Invoker is the slice of *plugin.Client a decision runtime needs — a seam
// so the runtime is testable without a subprocess.
type Invoker interface {
	Invoke(ctx context.Context, req plugin.InvokeRequest) (map[string]any, error)
}

// starter is the optional half of *plugin.Client that (re)launches the
// subprocess. The client binds the process to the context that DIALS it, so
// a runtime that let a per-call timeout context do the dialing would have
// its process killed when every decision returned — and the client's
// crash-loop guard would park it after a handful of calls. Starting with the
// runtime's own long-lived context first keeps the process up across calls;
// the per-call context then bounds only the call.
type starter interface {
	Start(ctx context.Context) error
}

// DefaultTimeout bounds one decide call. A System One model answers in well
// under a second; a runtime that has not answered in this long is failing,
// and the step should move to its next candidate rather than wait.
const DefaultTimeout = 30 * time.Second

// Runtime is one configured decision runtime.
type Runtime struct {
	// Name is the runtimes: entry name.
	Name string
	// Protocols are the decision protocols it declared.
	Protocols []string
	// Timeout bounds each call; zero is DefaultTimeout.
	Timeout time.Duration

	client Invoker
	conn   map[string]any
	// life is the context the subprocess lives under (see starter).
	life context.Context
}

// New wraps a plugin client as a decision runtime. conn is the runtime's
// resolved connection (credentials), handed to every call. The subprocess
// lives until the client is closed (the plugin manager closes it at
// shutdown).
func New(name string, protocols []string, client Invoker, conn map[string]any) *Runtime {
	return &Runtime{Name: name, Protocols: append([]string(nil), protocols...), client: client, conn: conn,
		life: context.Background()}
}

// invoke makes one call: the process is (re)started under the runtime's own
// lifetime, the call is bounded by the timeout. One retry covers exactly one
// case — the connection closed under the call (a process that exited, e.g.
// the boot-time describe process being torn down just as the first call
// arrived); the client has torn it down by then, so the retry re-launches.
// Any other failure is the answer, and the step moves to its next candidate.
func (r *Runtime) invoke(ctx context.Context, req plugin.InvokeRequest) (map[string]any, error) {
	var out map[string]any
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if s, ok := r.client.(starter); ok {
			if serr := s.Start(r.life); serr != nil {
				return nil, serr
			}
		}
		cctx, cancel := context.WithTimeout(ctx, r.timeout())
		out, err = r.client.Invoke(cctx, req)
		cancel()
		if err == nil || !errors.Is(err, acp.ErrClosed) || ctx.Err() != nil {
			return out, err
		}
	}
	return out, err
}

// IsDecisionRuntime reports whether a runtime plugin's declaration makes it
// a decision runtime: it declares at least one decision protocol and serves
// the decide verb.
func IsDecisionRuntime(decl *plugin.Decl) bool {
	if decl == nil || len(decl.Protocols) == 0 {
		return false
	}
	for _, v := range decl.Verbs {
		if v.Name == sdk.VerbDecide {
			return true
		}
	}
	return false
}

// Speaks reports whether the runtime answers protocol natively.
func (r *Runtime) Speaks(protocol string) bool {
	for _, p := range r.Protocols {
		if p == protocol {
			return true
		}
	}
	return false
}

// Answer is a decision runtime's validated reply.
type Answer struct {
	// Answers are the v1 answers, validated against the questions asked.
	Answers map[string]any
	// Model is the model the runtime reports it answered with (it may
	// resolve an alias like jev-latest to a concrete id). Falls back to the
	// requested model when the runtime does not say.
	Model string
}

// Decide asks the runtime one question set.
func (r *Runtime) Decide(ctx context.Context, protocol string, req systemone.Request) (Answer, error) {
	if !r.Speaks(protocol) {
		return Answer{}, fmt.Errorf("decision runtime %q does not speak %s (it declared: %s)", r.Name, protocol, strings.Join(r.Protocols, ", "))
	}
	opts := req.Wire()
	opts["protocol"] = protocol
	out, err := r.invoke(ctx, plugin.InvokeRequest{
		Instance: r.Name, Verb: sdk.VerbDecide, Options: opts, Connection: r.conn,
	})
	if err != nil {
		return Answer{}, fmt.Errorf("decision runtime %q: %w", r.Name, err)
	}
	answers, err := systemone.ValidateAnswers(req.Questions, out["answers"])
	if err != nil {
		return Answer{}, fmt.Errorf("decision runtime %q returned an invalid answer: %w", r.Name, err)
	}
	model, _ := out["model"].(string)
	if model == "" {
		model = req.Model
	}
	return Answer{Answers: answers, Model: model}, nil
}

// List implements models.Lister: the runtime's roster, from its models verb.
func (r *Runtime) List(ctx context.Context) (models.Roster, error) {
	out, err := r.invoke(ctx, plugin.InvokeRequest{
		Instance: r.Name, Verb: sdk.VerbModels, Connection: r.conn,
	})
	if err != nil {
		return nil, fmt.Errorf("decision runtime %q: list models: %w", r.Name, err)
	}
	list, _ := out["models"].([]any)
	roster := make(models.Roster, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		if strings.TrimSpace(id) == "" {
			continue
		}
		name, _ := m["name"].(string)
		released, _ := m["released"].(string)
		roster = append(roster, models.Model{ID: id, Name: name, Released: released})
	}
	if len(roster) == 0 {
		return nil, models.ErrNoDiscovery
	}
	return roster.SortNewestFirst(), nil
}

func (r *Runtime) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return DefaultTimeout
}

// Set is every configured decision runtime, by runtimes: name.
type Set map[string]*Runtime

// Get returns a runtime by name.
func (s Set) Get(name string) (*Runtime, bool) {
	r, ok := s[name]
	return r, ok
}

// DecisionOnly reports whether name is a decision runtime (resolver hook).
func (s Set) DecisionOnly(name string) bool {
	_, ok := s[name]
	return ok
}

// NativeProtocols reports a runtime's declared protocols (resolver hook).
func (s Set) NativeProtocols(name string) []string {
	if r, ok := s[name]; ok {
		return r.Protocols
	}
	return nil
}

// Names lists the runtimes, sorted.
func (s Set) Names() []string {
	out := make([]string, 0, len(s))
	for n := range s {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
