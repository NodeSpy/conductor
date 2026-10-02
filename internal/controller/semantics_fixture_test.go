package controller

import "github.com/NodeSpy/conductor/internal/core/coretest"

// Triggers this package's tests build by kind name carry the semantics their
// source declares, as they do in production.
func init() { coretest.UseDeclaredSemantics() }
