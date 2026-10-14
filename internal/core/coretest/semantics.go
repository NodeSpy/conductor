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

// forgeDecl and chatDecl are snapshots of two connector plugins' describe:
// a forge (the official github plugin's, type "github") and a chat service
// (the official slack plugin's, type "slack"). Conductor's tests drive the
// engine, flow and config layers with the declarations production plugins
// really send; nothing here is consulted at runtime, where every trigger
// carries its own connector's declaration.
//
//go:embed testdata/forge-decl.json
var forgeDecl []byte

//go:embed testdata/chat-decl.json
var chatDecl []byte

var (
	once     sync.Once
	declared map[string]*sdk.EventSemantics
)

func load() {
	once.Do(func() {
		declared = map[string]*sdk.EventSemantics{}
		for _, raw := range [][]byte{chatDecl, forgeDecl} { // the forge's wins a shared name
			var d sdk.Decl
			if err := json.Unmarshal(raw, &d); err != nil {
				panic("coretest: fixture declaration: " + err.Error())
			}
			for _, ev := range d.Events {
				declared[ev.Name] = ev.Semantics
			}
		}
	})
}

// ForgeDecl is the fixture forge connector's declaration (a fresh copy).
func ForgeDecl() sdk.Decl { return decode(forgeDecl) }

// ChatDecl is the fixture chat connector's declaration (a fresh copy).
func ChatDecl() sdk.Decl { return decode(chatDecl) }

func decode(raw []byte) sdk.Decl {
	var d sdk.Decl
	if err := json.Unmarshal(raw, &d); err != nil {
		panic("coretest: fixture declaration: " + err.Error())
	}
	return d
}

// DeclaredSemantics is the semantics a fixture connector declares for t's
// kind (nil for a kind neither declares) — what the source adapter attaches
// to a trigger of that kind in production.
func DeclaredSemantics(t core.Trigger) *sdk.EventSemantics {
	load()
	return declared[t.Kind]
}

// UseDeclaredSemantics installs DeclaredSemantics as the fallback for
// triggers a test builds without semantics.
func UseDeclaredSemantics() { core.SemanticsFallback = DeclaredSemantics }

// Forge and Chat serve the fixture declarations over the plugin contract, as
// in-process connectors (connector.RegisterInProcessConnector(coretest.Forge)
// in a test package's init). Verbs answer through Respond (default: an empty
// result) and are recorded; the source emits nothing; a delivery's body is
// the decoded event itself (one sdk.SourceEvent, or a list) — decoding a
// forge's own payloads is the plugin's job, tested where the plugin lives.
var (
	Forge = &ForgeHandler{raw: forgeDecl}
	Chat  = &ForgeHandler{raw: chatDecl}
)

// ForgeHandler is a fixture connector's sdk.Handler.
type ForgeHandler struct {
	raw     []byte
	mu      sync.Mutex
	calls   []sdk.InvokeRequest
	respond func(sdk.InvokeRequest) (sdk.InvokeResult, error)
}

// Describe returns the fixture declaration.
func (f *ForgeHandler) Describe() sdk.Decl { return decode(f.raw) }

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
