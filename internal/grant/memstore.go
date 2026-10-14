package grant

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemStore is an in-process Store. Grants are short-lived approvals, so a
// daemon restart dropping them is the safe failure: the case still records
// the approval and the gate can mint again; a grant never outlives the
// process that checked who approved it.
type MemStore struct {
	mu     sync.Mutex
	now    func() time.Time
	grants map[string]*entry
}

type entry struct {
	spec    GrantSpec
	used    bool
	revoked bool
}

// NewMemStore returns an empty store using the wall clock.
func NewMemStore() *MemStore { return NewMemStoreClock(time.Now) }

// NewMemStoreClock returns an empty store reading time from now (tests).
func NewMemStoreClock(now func() time.Time) *MemStore {
	return &MemStore{now: now, grants: map[string]*entry{}}
}

var _ Store = (*MemStore)(nil)

// Mint implements Store.
func (s *MemStore) Mint(_ context.Context, g GrantSpec) (GrantSpec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if err := g.Validate(now); err != nil {
		return GrantSpec{}, err
	}
	g.ID = NewID()
	g.Issued = now
	g = clone(g)
	s.grants[g.ID] = &entry{spec: g}
	s.sweepLocked(now)
	return clone(g), nil
}

// Get implements Store.
func (s *MemStore) Get(_ context.Context, id string) (GrantSpec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.grants[id]
	if !ok {
		return GrantSpec{}, ErrNotFound
	}
	return clone(e.spec), nil
}

// Find implements Store.
func (s *MemStore) Find(_ context.Context, p Presented) ([]GrantSpec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var out []GrantSpec
	for _, e := range s.grants {
		if e.liveErr(now) == nil && e.spec.Matches(p) {
			out = append(out, clone(e.spec))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Expiry.Equal(out[j].Expiry) {
			return out[i].Expiry.Before(out[j].Expiry)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Consume implements Store.
func (s *MemStore) Consume(_ context.Context, id string, p Presented) (GrantSpec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.grants[id]
	if !ok {
		return GrantSpec{}, ErrNotFound
	}
	if err := e.liveErr(s.now()); err != nil {
		return GrantSpec{}, fmt.Errorf("%w (%s)", err, id)
	}
	if !e.spec.Matches(p) {
		return GrantSpec{}, fmt.Errorf("%w (%s)", ErrMismatch, id)
	}
	if e.spec.SingleUse {
		e.used = true
	}
	return clone(e.spec), nil
}

// Revoke implements Store.
func (s *MemStore) Revoke(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.grants[id]
	if !ok {
		return ErrNotFound
	}
	e.revoked = true
	return nil
}

// ByCase implements Store.
func (s *MemStore) ByCase(_ context.Context, caseID string) ([]GrantSpec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []GrantSpec
	for _, e := range s.grants {
		if caseID != "" && e.spec.CaseID == caseID {
			out = append(out, clone(e.spec))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Issued.Before(out[j].Issued) || out[i].Issued.Equal(out[j].Issued) && out[i].ID < out[j].ID
	})
	return out, nil
}

func (e *entry) liveErr(now time.Time) error {
	switch {
	case e.revoked:
		return ErrRevoked
	case e.used:
		return ErrConsumed
	case !now.Before(e.spec.Expiry):
		return ErrExpired
	}
	return nil
}

// sweepLocked drops grants that expired more than a day ago, so a long-lived
// daemon's store stays bounded while recent history stays inspectable.
func (s *MemStore) sweepLocked(now time.Time) {
	for id, e := range s.grants {
		if now.Sub(e.spec.Expiry) > MaxTTL {
			delete(s.grants, id)
		}
	}
}

// clone deep-copies a spec so callers can never mutate a stored grant.
func clone(g GrantSpec) GrantSpec {
	g.Argv.Words = append([]string(nil), g.Argv.Words...)
	if g.Argv.Flags != nil {
		f := make(map[string][]string, len(g.Argv.Flags))
		for k, v := range g.Argv.Flags {
			f[k] = append([]string(nil), v...)
		}
		g.Argv.Flags = f
	}
	g.FileDigests = append([]FileDigest(nil), g.FileDigests...)
	return g
}
