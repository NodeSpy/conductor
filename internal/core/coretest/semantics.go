// Package coretest is test support for packages that build triggers by kind
// name: it supplies the declared semantics a real source would attach. Only
// tests import it.
package coretest

import (
	"sync"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/pkg/githubkit/ghplugin"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

var (
	once     sync.Once
	declared map[string]*sdk.EventSemantics
)

// DeclaredSemantics is the semantics the github connector declares for kind
// (nil for a kind it does not declare) — what the source adapter attaches to
// a trigger of that kind in production.
func DeclaredSemantics(t core.Trigger) *sdk.EventSemantics {
	once.Do(func() {
		declared = map[string]*sdk.EventSemantics{}
		for _, ev := range ghplugin.Decl().Events {
			declared[ev.Name] = ev.Semantics
		}
	})
	return declared[t.Kind]
}

// UseDeclaredSemantics installs DeclaredSemantics as the fallback for
// triggers a test builds without semantics.
func UseDeclaredSemantics() { core.SemanticsFallback = DeclaredSemantics }
