package flow

import (
	"context"
	"errors"
)

// A run's context ends for two different reasons, and they must not be
// confused: the daemon shutting down (the run is interrupted — its record
// stays and it resumes on restart, so nothing about it is over), or something
// INSIDE the run giving out — a step timeout, the run's budget deadline (a
// failure). Run stamps the context it was handed, which only a shutdown
// cancels, so either can be told from the other at any depth.

type shutdownKey struct{}

// withShutdownSignal records parent as the context whose cancellation means
// "the daemon is shutting down".
func withShutdownSignal(ctx, parent context.Context) context.Context {
	return context.WithValue(ctx, shutdownKey{}, parent)
}

// shuttingDown reports whether the daemon is shutting down under this run.
func shuttingDown(ctx context.Context) bool {
	p, _ := ctx.Value(shutdownKey{}).(context.Context)
	return p != nil && errors.Is(p.Err(), context.Canceled)
}
