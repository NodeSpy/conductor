// Package coretest is test support for packages that build triggers by kind
// name or configs naming a forge connector: it supplies the declarations a
// real forge plugin sends. Only tests import it.
package coretest

import (
	"context"
	_ "embed"
	"encoding/json"
	"sync"

	"github.com/NodeSpy/conductor/internal/core"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// forgeDecl is a snapshot of a forge connector plugin's describe (the
// official github plugin's, type "github"). Conductor's tests drive the
// engine, flow and config layers with the declarations a production plugin
// really sends; nothing here is consulted at runtime, where every trigger
// carries its own connector's declaration.
//
//go:embed testdata/forge-decl.json
var forgeDecl []byte

var (
	once     sync.Once
	decl     sdk.Decl
	declared map[string]*sdk.EventSemantics
)

func load() {
	once.Do(func() {
		if err := json.Unmarshal(forgeDecl, &decl); err != nil {
			panic("coretest: forge-decl.json: " + err.Error())
		}
		declared = map[string]*sdk.EventSemantics{}
		for _, ev := range decl.Events {
			declared[ev.Name] = ev.Semantics
		}
	})
}

// ForgeDecl is the fixture forge connector's declaration (a fresh copy).
func ForgeDecl() sdk.Decl {
	load()
	var d sdk.Decl
	_ = json.Unmarshal(forgeDecl, &d)
	return d
}

// DeclaredSemantics is the semantics the fixture forge connector declares for
// t's kind (nil for a kind it does not declare) — what the source adapter
// attaches to a trigger of that kind in production.
func DeclaredSemantics(t core.Trigger) *sdk.EventSemantics {
	load()
	return declared[t.Kind]
}

// UseDeclaredSemantics installs DeclaredSemantics as the fallback for
// triggers a test builds without semantics.
func UseDeclaredSemantics() { core.SemanticsFallback = DeclaredSemantics }

// Forge serves the fixture declaration over the plugin contract, as an
// in-process connector (connector.RegisterInProcessConnector(coretest.Forge)
// in a test package's init). Verbs answer through Respond (default: an empty
// result) and are recorded; the source emits nothing; a delivery's body is
// the decoded event itself (one sdk.SourceEvent, or a list) — decoding a
// forge's own payloads is the plugin's job, tested where the plugin lives.
var Forge = &ForgeHandler{}

// ForgeHandler is the Forge connector's sdk.Handler.
type ForgeHandler struct {
	mu      sync.Mutex
	calls   []sdk.InvokeRequest
	respond func(sdk.InvokeRequest) (sdk.InvokeResult, error)
}

// Describe returns the fixture declaration.
func (f *ForgeHandler) Describe() sdk.Decl { return ForgeDecl() }

// Invoke records the call and answers it.
func (f *ForgeHandler) Invoke(req sdk.InvokeRequest) (sdk.InvokeResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	r := f.respond
	f.mu.Unlock()
	if r != nil {
		return r(req)
	}
	return sdk.InvokeResult{Outputs: map[string]any{}}, nil
}

// StartSource emits nothing and runs until the daemon stops it.
func (f *ForgeHandler) StartSource(ctx context.Context, _ sdk.StartSourceRequest, _ func(any) error) error {
	<-ctx.Done()
	return nil
}

// Translate decodes a fixture delivery: the body is the event (or events).
func (f *ForgeHandler) Translate(_ context.Context, req sdk.TranslateRequest) (sdk.TranslateResult, error) {
	var evs []sdk.SourceEvent
	if err := json.Unmarshal([]byte(req.Body), &evs); err != nil {
		var ev sdk.SourceEvent
		if err := json.Unmarshal([]byte(req.Body), &ev); err != nil {
			return sdk.TranslateResult{}, sdk.Fail(sdk.CodeInvalid, "fixture delivery: "+err.Error(), nil)
		}
		evs = []sdk.SourceEvent{ev}
	}
	return sdk.TranslateResult{Events: evs}, nil
}

// Respond sets how verbs answer (nil restores the empty result) and clears
// the recorded calls.
func (f *ForgeHandler) Respond(r func(sdk.InvokeRequest) (sdk.InvokeResult, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.respond, f.calls = r, nil
}

// Calls returns the verb calls recorded since the last Respond.
func (f *ForgeHandler) Calls() []sdk.InvokeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sdk.InvokeRequest(nil), f.calls...)
}
