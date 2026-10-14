package main

import "github.com/NodeSpy/conductor/internal/grant"

// grantStore is the daemon's one grant store, shared by the member gate
// (which mints on approval) and the jail's broker (which consumes). In-memory
// by design: a grant is a short-lived approval, and a restart dropping it is
// the safe failure (the case keeps the approval record; an operator
// re-approves).
//
// The same declaration lives in members.go on feat/members: when the two
// meet, this file goes (a duplicate declaration until it does) and both sides
// share the one store.
var grantStore grant.Store = grant.NewMemStore()
