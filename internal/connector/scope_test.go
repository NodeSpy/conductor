package connector

import (
	"reflect"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
)

// The declaration side of resource scoping: what a connector says about its
// own options, and what it answers about a dispatch.

func TestScopedOptionsReportsOnlyTaggedOptions(t *testing.T) {
	vd := VerbDecl{
		Name: "post",
		Options: Schema{
			"channel": {Type: TString, Scope: "channel"},
			"user":    {Type: TString, Scope: "user"},
			"text":    {Type: TString, Required: true},
		},
	}
	want := []ScopedOption{{Name: "channel", Dim: "channel"}, {Name: "user", Dim: "user"}}
	if got := vd.ScopedOptions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("scoped options must be the tagged ones, sorted: %v", got)
	}
}

// The tagging that closes the reported gap, asserted on the real declarations
// rather than on a fixture — these are the option names an operator's grant
// and an agent's call actually use.
func TestBuiltinConnectorsTagTheirDestinations(t *testing.T) {
	for _, tc := range []struct{ typ, verb, opt, dim string }{
		{"slack", "post", "channel", "channel"},
		{"slack", "react", "channel", "channel"},
		{"slack", "post", "user", "user"},
		{"discord", "post", "channel", "channel"},
		{"github", "submit_review", "repo", "repo"},
		{"github", "put_file", "repo", "repo"},
		{"kv", "set", "store", "store"},
		{"sql", "query", "store", "store"},
		{"blob", "put", "path", "path"},
	} {
		decl, ok := TypeDeclFor(tc.typ)
		if !ok {
			t.Errorf("connector type %q is not registered", tc.typ)
			continue
		}
		vd, ok := decl.Verb(tc.verb)
		if !ok {
			t.Errorf("%s has no verb %q", tc.typ, tc.verb)
			continue
		}
		if got := vd.Options[tc.opt].Scope; got != tc.dim {
			t.Errorf("%s.%s option %q: Scope=%q, want %q — an untagged destination is an ungated one",
				tc.typ, tc.verb, tc.opt, got, tc.dim)
		}
	}
	// Content is NOT a destination: tagging it would gate the message body.
	slack, _ := TypeDeclFor("slack")
	post, _ := slack.Verb("post")
	if s := post.Options["text"].Scope; s != "" {
		t.Errorf("slack.post text must stay untagged, got Scope=%q", s)
	}
}

func TestContextScopeResolutionOrder(t *testing.T) {
	trig := core.Trigger{
		Target:  core.Target{Repo: "acme/app"},
		Context: map[string]any{"slack": map[string]any{"channel": "#from-event"}},
	}

	// 1. the implementation's hook.
	sl := &Instance{Name: "slack", Decl: slackDecl, Impl: &slackImpl{name: "slack"}}
	if got := sl.ContextScope("channel", trig); got != "#from-event" {
		t.Errorf("the connector's own hook must win: %q", got)
	}
	// 2. the repo dimension, for every connector (core models the target).
	if got := sl.ContextScope("repo", trig); got != "acme/app" {
		t.Errorf("the dispatch's target repo must answer the repo dimension: %q", got)
	}
	// 3. the operator's configured default for an option in the dimension.
	def := &Instance{
		Name: "slack", Decl: slackDecl, Impl: &slackImpl{name: "slack"},
		DefaultOptions: map[string]any{"channel": "#configured-default"},
	}
	if got := def.ContextScope("channel", core.Trigger{}); got != "#configured-default" {
		t.Errorf("the connector's configured default must be implicitly in scope: %q", got)
	}
	// …but a live event still wins over it.
	if got := def.ContextScope("channel", trig); got != "#from-event" {
		t.Errorf("the event's own channel must win over the default: %q", got)
	}
	// Nothing to say → "", which the caller reads as deny-unless-listed.
	if got := sl.ContextScope("store", trig); got != "" {
		t.Errorf("an unanswerable dimension must be empty, got %q", got)
	}
	if got := sl.ContextScope("channel", core.Trigger{}); got != "" {
		t.Errorf("no event and no default is empty, got %q", got)
	}
}

func TestScopeDimsListsWhatAConnectorDeclares(t *testing.T) {
	decl, _ := TypeDeclFor("slack")
	if got := decl.ScopeDims(); !reflect.DeepEqual(got, []string{"channel", "user"}) {
		t.Fatalf("slack dimensions: %v", got)
	}
	decl, _ = TypeDeclFor("kv")
	if got := decl.ScopeDims(); !reflect.DeepEqual(got, []string{"store"}) {
		t.Fatalf("kv dimensions: %v", got)
	}
}
