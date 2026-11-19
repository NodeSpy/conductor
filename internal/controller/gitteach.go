package controller

import (
	"context"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// A TEACHING launch learns its repository's git fetch dependencies
// (engine/gitfetch.go). Its credential covers any repository on the host
// (TeachGitAuth), the proxy reports each one the forge served it, and when
// the launch's step succeeds and its cache is published (TaughtGit) that
// set becomes the repository's learned dependencies for the launch's key,
// replacing what the key's previous teaching run learned. Every other
// launch on the repository gets the repository plus those dependencies.

type teaching struct {
	repo, key string
	fetched   []string
}

var (
	teachMu sync.Mutex
	teachBy = map[string]*teaching{} // dispatch id → what its launches fetched
)

// teachingGitAuth mints a teaching launch's credentials and records what
// they fetch under its dispatch.
func teachingGitAuth(dispatch, repo, key string) []sandbox.GitAuth {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	auth := TeachGitAuth(ctx, repo)
	teachMu.Lock()
	t := teachBy[dispatch]
	if t == nil {
		t = &teaching{repo: repo, key: key}
		teachBy[dispatch] = t
	}
	teachMu.Unlock()
	for i := range auth {
		host := auth[i].Host
		auth[i].OnFetch = func(r string) {
			teachMu.Lock()
			t.fetched = append(t.fetched, host+r)
			teachMu.Unlock()
		}
	}
	return auth
}

// TaughtGit ends a dispatch's teaching: published — its step succeeded and
// its cache was published — records what its launches fetched as the
// repository's learned dependencies for its key; otherwise it is dropped.
func TaughtGit(dispatch string, published bool) {
	teachMu.Lock()
	t := teachBy[dispatch]
	delete(teachBy, dispatch)
	var fetched []string
	if t != nil {
		fetched = append([]string{}, t.fetched...)
	}
	teachMu.Unlock()
	if t == nil || !published || TeachGitLearned == nil {
		return
	}
	TeachGitLearned(t.repo, "cache:"+t.key, fetched)
}
