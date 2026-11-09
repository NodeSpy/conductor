package plugin

import (
	"fmt"
	"path"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
)

// Consent to a wider declaration (docs/wiki/Plugins.md "Updates that ask for
// more"). An update whose declaration widens what the installed build
// declared is held: fetched and verified, recorded here, never run, while the
// installed build keeps running. `conductor plugin approve <name>@<sha>`
// records the user's yes to that build; the held build is then installed like any update, and the
// next reload or restart runs it. Narrower or unchanged declarations apply
// as usual, and so does a verified release of an official plugin
// (autoAccepted): it is installed, and what it added is logged.

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
	p.PreJail = p.PreJail || s.preJail(p.Key, p.Source)
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

// accepts reports that rec is installed even where it declares more than
// the build it replaces: the user approved exactly this build (approved),
// or it is a verified release of an official plugin (autoAccepted).
func (s *InstallState) accepts(rec Installed) bool {
	return s.approved(rec) || autoAccepted(rec)
}

// autoAccepted reports that rec is a release of the official plugin repo
// (config.IsOfficialSource, the source plugin_trust admits with no entry)
// whose binary was verified against the release's own checksums
// (Installed.ReleaseVerified). Its wider declaration is taken without a
// hold: the user already trusts that repo by default, and holding its
// updates only leaves a plugin running on a record its newer builds
// outgrew. It still runs jailed to exactly what it declares, and what it
// added is logged (logAutoAccepted). A third-party, local or unverified
// build is held as before.
func autoAccepted(rec Installed) bool {
	return rec.ReleaseVerified && config.IsOfficialSource(rec.Source)
}

// logAutoAccepted logs a widening that autoAccepted let through without the
// user's approval, naming what it added.
func logAutoAccepted(opts Options, name string, rec Installed, replaces string, widens []string) {
	opts.logf("plugin %s: %s asks for more than %s — %s. Accepted without a hold: it is a verified release of the official plugin repo",
		name, rec.Resolved, replaces, strings.Join(widens, "; "))
}

// ApproveArg is what `conductor plugin approve` takes to accept rec, held
// for the plugin named name: the name and the build's sha (its tag when it
// has none), so the approval is of that build and not of one held after
// it was listed.
func ApproveArg(name string, rec Installed) string {
	if rec.Sha256 == "" {
		return name + "@" + rec.Resolved
	}
	return name + "@" + rec.Sha256[:min(12, len(rec.Sha256))]
}

// Approve accepts the held update(s) that arg names: "<name>@<build>", the
// plugin (its name, or its install key) and the build the user was shown —
// a prefix of its sha (7 characters or more), its tag, or the tag's version.
// Each is installed as the current build (when its binary is still on disk;
// otherwise the approval is recorded and the next update pass installs it
// without holding). A held update that is not that build (one held since the
// user looked) is not approved. It returns what was approved.
func (s *InstallState) Approve(arg string) ([]Pending, error) {
	if s == nil {
		return nil, fmt.Errorf("no install state")
	}
	name, build, _ := strings.Cut(arg, "@")
	var waiting []string
	for _, q := range s.Pending {
		if q.Name == name || q.Key == name {
			waiting = append(waiting, ApproveArg(q.Name, q.Installed)+" ("+q.Resolved+")")
		}
	}
	if len(waiting) == 0 {
		var names []string
		for _, q := range s.Pending {
			names = append(names, q.Name)
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("plugin %s: no update is waiting for approval", name)
		}
		return nil, fmt.Errorf("plugin %s: no update is waiting for approval (waiting: %s)", name, strings.Join(names, ", "))
	}
	if build == "" {
		return nil, fmt.Errorf("plugin %s: say which build you approve, as `conductor plugin approve` lists it: %s", name, strings.Join(waiting, ", "))
	}
	var done []Pending
	kept := s.Pending[:0]
	for _, q := range s.Pending {
		if (q.Name != name && q.Key != name) || !isBuild(q.Installed, build) {
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
		return nil, fmt.Errorf("plugin %s: %s is not the update waiting for approval — that is %s; review it with `conductor plugin approve`", name, build, strings.Join(waiting, ", "))
	}
	return done, nil
}

// isBuild: build names rec (Approve).
func isBuild(rec Installed, build string) bool {
	if build == rec.Resolved || build == path.Base(rec.Resolved) {
		return true
	}
	return len(build) >= 7 && strings.HasPrefix(rec.Sha256, strings.ToLower(build))
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
