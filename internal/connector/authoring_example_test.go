package connector

// This file is the DOCUMENTED TEMPLATE for authoring a new in-tree connector
// (#36 §22) — a complete, compiling, tested miniature you can copy into
// <yourtype>.go and grow. The wiki page Authoring-Connectors walks through
// it section by section. Because it lives in a _test file, it ships in no
// binary, but it can never rot: `go test ./internal/connector` builds and
// exercises it on every change to the contract.
//
// The shape of every connector:
//
//  1. a TypeDecl — the self-description everything else reads: events (with
//     filter/context schemas), verbs (with option/output schemas), and the
//     documented connection fields;
//  2. an Impl — Validate / Invoke / Source / DeclaredEvents over one
//     configured instance;
//  3. a Builder registered via RegisterType in init().
//
// Keep the declaration honest: `conductor validate`, `conductor schema`, and
// the flow runner's option validation all read it — an undeclared option is
// a load error for the user, a declared-but-unhandled one is a bug report.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// exampleDecl declares the "authoring-example" type: one event, two verbs.
var exampleDecl = &TypeDecl{
	Type: "authoring-example",
	Desc: "Template connector: the copyable starting point for a new type.",
	// Connection documents the instance's own config keys for
	// `conductor schema` (parsing them happens in the builder below).
	Connection: Schema{
		"greeting": {Type: TString, Desc: "prefix for every say (default \"hello\")"},
		"token":    {Type: TString, Desc: "credential — resolve ${…}/vault refs via deps.Secrets"},
	},
	Events: []EventDecl{{
		Name: "waved",
		Desc: "someone waved at us",
		// filters: keys legal under a trigger's `filters:` for this event.
		Filters: Schema{"from": {Type: TString, Desc: "only waves from this sender"}},
		// context: the facts the event publishes into the template scope.
		Context: Schema{"sender": {Type: TString}, "emphatic": {Type: TBool}},
	}},
	// Filter makes `filters:` evaluate uniformly in the flow runner. Types
	// whose lowered integration evaluates its own filters leave this nil.
	Filter: func(event string, filters, trigCtx map[string]any) (bool, error) {
		if want, _ := filters["from"].(string); want != "" {
			got, _ := trigCtx["sender"].(string)
			return got == want, nil
		}
		return true, nil
	},
	Verbs: []VerbDecl{
		{
			Name: "say", Desc: "compose a greeting",
			Options: Schema{
				"name":  {Type: TString, Required: true, Desc: "who to greet"},
				"shout": {Type: TBool, Desc: "UPPERCASE the result"},
			},
			Outputs: Schema{"text": {Type: TString}},
		},
		{
			// A verb with declared binary IO (#36 §21): the flow runner
			// resolves a blob handle on `file` to the blob's on-disk path
			// before Invoke reaches us.
			Name: "measure", Desc: "size of a staged artifact",
			Options:  Schema{"file": {Type: TAny, Required: true}},
			Outputs:  Schema{"bytes": {Type: TInt}},
			BinaryIn: []string{"file"},
		},
	},
}

// exampleImpl is one configured instance. Parse the connection ONCE in the
// builder; Invoke should only ever see ready-to-use fields.
type exampleImpl struct {
	greeting string
}

// exampleConn is the instance's own YAML shape — the keys documented in
// exampleDecl.Connection. ref.Decode fills it from the connector's raw node
// (the `type:`/`enabled:`/`options:`/`policy:` header keys are shared and
// already parsed; everything else is yours).
type exampleConn struct {
	Greeting string `yaml:"greeting"`
	Token    string `yaml:"token"`
}

// newExampleImpl is the Builder. Return an error for anything that makes the
// instance unusable at runtime (an unresolvable secret, a bad URL): Build
// DISABLES the connector with that reason and the daemon keeps booting — a
// bad connector must never crash-loop the box. Structural nonsense should
// instead fail Validate (a config bug, reported at load). Resolve secret
// references (${…}, vault URIs) through deps.Secrets so the values are
// tracked for redaction everywhere they might leak.
func newExampleImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var cc exampleConn
	if err := ref.Decode(&cc); err != nil {
		return nil, fmt.Errorf("authoring-example: %w", err)
	}
	if cc.Greeting == "" {
		cc.Greeting = "hello"
	}
	return &exampleImpl{greeting: cc.Greeting}, nil
}

func init() { RegisterType(exampleDecl, newExampleImpl) }

func (e *exampleImpl) Validate() error { return nil }

// DeclaredEvents returns instance-defined event names (cron schedules, rss
// feeds). Static types return nil.
func (e *exampleImpl) DeclaredEvents() []string { return nil }

// Source lowers this connector's triggers into the integration providing
// the event transport. A verb-only type returns (nil, nil); a type whose
// events arrive by webhook/poll builds (or reuses) an integration here.
// Returning an error when triggers reference an event the connection can't
// deliver beats silently never firing.
func (e *exampleImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	return nil, nil
}

// Invoke runs one verb with FINAL options (defaults merged, templates
// rendered, secret handles resolved). Return the outputs the schema
// declares; fire-and-forget verbs return a small ack map.
func (e *exampleImpl) Invoke(_ context.Context, verb string, opts map[string]any) (map[string]any, error) {
	switch verb {
	case "say":
		name, _ := opts["name"].(string)
		text := e.greeting + ", " + name
		if shout, _ := opts["shout"].(bool); shout {
			text = strings.ToUpper(text)
		}
		return map[string]any{"text": text}, nil
	case "measure":
		path, _ := opts["file"].(string)
		return map[string]any{"bytes": len(path)}, nil // a real type would stat/stream the file
	}
	return nil, fmt.Errorf("authoring-example: unknown verb %q", verb)
}

// ---------------------------------------------------------------------------
// The template's tests — the conventions every connector should cover: build
// through the real registry, invoke through the Instance (defaults merge,
// rate limits apply), validate options against the schema, evaluate filters.
// ---------------------------------------------------------------------------

func TestAuthoringExampleRoundTrip(t *testing.T) {
	reg := buildSinkRegistry(t, `
connectors:
  hello:
    use: authoring-example
    greeting: howdy                  # a connection field (ref.Decode)
    options: { shout: false }        # connector-default verb options; calls merge over them
`)
	in, ok := reg.Get("hello")
	if !ok || !in.Enabled {
		t.Fatalf("instance: %+v %v", in, ok)
	}

	out, err := in.Invoke(context.Background(), "say", map[string]any{"name": "world"})
	if err != nil {
		t.Fatal(err)
	}
	if out["text"] != "howdy, world" {
		t.Fatalf("say: %+v", out)
	}
	// A call option wins over the connector default.
	out, err = in.Invoke(context.Background(), "say", map[string]any{"name": "world", "shout": true})
	if err != nil || out["text"] != "HOWDY, WORLD" {
		t.Fatalf("shout: %+v %v", out, err)
	}
	// An undeclared verb is refused by the Instance before Invoke.
	if _, err := in.Invoke(context.Background(), "yodel", nil); err == nil ||
		!strings.Contains(err.Error(), `no verb "yodel"`) {
		t.Fatalf("unknown verb: %v", err)
	}
}

func TestAuthoringExampleSchemas(t *testing.T) {
	decl, ok := TypeDeclFor("authoring-example")
	if !ok {
		t.Fatal("type not registered")
	}
	say, _ := decl.Verb("say")
	// Option validation: unknown keys and type mismatches are load errors
	// for the user — exactly what `conductor validate` runs.
	if err := ValidateCallOptions("here", say.Options, map[string]any{"name": "x"}, nil); err != nil {
		t.Fatalf("valid call: %v", err)
	}
	if err := ValidateCallOptions("here", say.Options, map[string]any{"nmae": "x"}, nil); err == nil {
		t.Fatal("typo'd option must fail validation")
	}
	if err := ValidateCallOptions("here", say.Options, map[string]any{"name": "x", "shout": "yes"}, nil); err == nil {
		t.Fatal("type mismatch must fail validation")
	}

	// Filters evaluate through the declared Filter func.
	ev, _ := decl.Event("waved")
	if ev.Name != "waved" {
		t.Fatalf("event: %+v", ev)
	}
	match, err := decl.Filter("waved", map[string]any{"from": "ada"}, map[string]any{"sender": "ada"})
	if err != nil || !match {
		t.Fatalf("filter match: %v %v", match, err)
	}
	if match, _ := decl.Filter("waved", map[string]any{"from": "ada"}, map[string]any{"sender": "bob"}); match {
		t.Fatal("filter must reject a different sender")
	}
}
