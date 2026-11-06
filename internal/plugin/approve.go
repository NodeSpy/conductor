package plugin

import (
	"fmt"
	"strings"
)

// Consent to a wider declaration (docs/wiki/Plugins.md "Updates that ask for
// more"). An update whose declaration widens what the installed build
// declared is held: fetched and verified, recorded here, never run, while the
// installed build keeps running. `conductor plugin approve <name>` records
// the user's yes; the held build is then installed like any update, and the
// next reload or restart runs it. Narrower or unchanged declarations apply
// as usual.

// Pending is one held update.
type Pending struct {
	Installed `yaml:",inline"`
	// Replaces is the installed release it would replace.
	Replaces string `yaml:"replaces,omitempty"`
	// Widens is what it asks for beyond Replaces, in plain words.
	Widens []string `yaml:"widens,omitempty"`
	// Approved records the user's approval (`plugin approve`).
	Approved bool `yaml:"approved,omitempty"`
}

// PendingUpdates lists the held updates.
func (s *InstallState) PendingUpdates() []Pending {
	if s == nil {
		return nil
	}
	return append([]Pending(nil), s.Pending...)
}

func (s *InstallState) putPending(p Pending) {
	if s == nil {
		return
	}
	for i, q := range s.Pending {
		if q.Key == p.Key && q.Source == p.Source {
			p.Approved = q.Approved && q.Sha256 == p.Sha256 && q.Resolved == p.Resolved
			s.Pending[i] = p
			return
		}
	}
	s.Pending = append(s.Pending, p)
}

func (s *InstallState) dropPending(key, source string) {
	if s == nil {
		return
	}
	kept := s.Pending[:0]
	for _, q := range s.Pending {
		if q.Key == key && q.Source == source {
			continue
		}
		kept = append(kept, q)
	}
	s.Pending = kept
}

// approved reports the user approved exactly this build (same release, same
// sha, same declaration).
func (s *InstallState) approved(rec Installed) bool {
	if s == nil {
		return false
	}
	for _, q := range s.Pending {
		if q.Approved && q.Key == rec.Key && q.Source == rec.Source && q.Resolved == rec.Resolved && q.Sha256 == rec.Sha256 &&
			len(q.Manifest.Widens(rec.Manifest)) == 0 && len(rec.Manifest.Widens(q.Manifest)) == 0 {
			return true
		}
	}
	return false
}

// Approve accepts the held update(s) of the plugin named name (its name, or
// its install key): each is installed as the current build (when its binary
// is still on disk; otherwise the approval is recorded and the next update
// pass installs it without holding). It returns what was approved.
func (s *InstallState) Approve(name string) ([]Pending, error) {
	if s == nil {
		return nil, fmt.Errorf("no install state")
	}
	var done []Pending
	kept := s.Pending[:0]
	for _, q := range s.Pending {
		if q.Name != name && q.Key != name {
			kept = append(kept, q)
			continue
		}
		q.Approved = true
		done = append(done, q)
		if binaryPresent(q.Path) {
			s.Put(q.Installed)
			continue
		}
		kept = append(kept, q)
	}
	s.Pending = kept
	if len(done) == 0 {
		var names []string
		for _, q := range s.Pending {
			names = append(names, q.Name)
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("plugin %s: no update is waiting for approval", name)
		}
		return nil, fmt.Errorf("plugin %s: no update is waiting for approval (waiting: %s)", name, strings.Join(names, ", "))
	}
	return done, nil
}

// findSha is the record of key from source with that sha.
func (s *InstallState) findSha(key, source, sha string) (Installed, bool) {
	if s == nil || sha == "" {
		return Installed{}, false
	}
	for _, p := range s.Plugins {
		if p.Key == key && p.Source == source && p.Sha256 == sha {
			return p, true
		}
	}
	return Installed{}, false
}
