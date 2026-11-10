package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// Consent (docs/wiki/Plugins.md "Updates that change what a plugin may
// do"). An update is compared with the build it would replace before it is
// installed. One that declares the same is installed. One whose declaration
// differs (anything added or removed) is held: fetched and verified into a
// directory of its own, recorded here, never run, while the installed build
// keeps running exactly as it was: same binary, same record. That holds for
// every source, the official repo included. The daemon announces it once
// (Pending.Announced); `plugin list`, `status` and the dashboard show it.
// `conductor plugin approve <name>` (or the dashboard) approves it: the held
// build replaces the installed one, and the next reload or restart runs it.
// `conductor plugin decline <name>` keeps the installed build and stops
// asking about that declaration (Pending.Declined). An approval is of a
// declaration, so a newer build declaring the same is approved too.

// Pending is one held update.
type Pending struct {
	Installed `yaml:",inline"`
	// Replaces is the installed release it would replace.
	Replaces string `yaml:"replaces,omitempty"`
	// Widens is what it declares beyond Replaces, in plain words.
	Widens []string `yaml:"widens,omitempty"`
	// Removes is what Replaces declared that it no longer does.
	Removes []string `yaml:"removes,omitempty"`
	// Announced records that the daemon told the user about this build, so
	// a restart does not tell them again.
	Announced bool `yaml:"announced,omitempty"`
	// Declined records that the user kept the installed build instead
	// (`plugin decline`): this declaration is not asked about again.
	Declined bool `yaml:"declined,omitempty"`
	// Approved records the user's approval (`plugin approve`).
	Approved bool `yaml:"approved,omitempty"`
}

// Waiting reports p waits for the user: neither approved nor declined.
func (p Pending) Waiting() bool { return !p.Approved && !p.Declined }

// PendingUpdates lists the held updates.
func (s *InstallState) PendingUpdates() []Pending {
	if s == nil {
		return nil
	}
	return append([]Pending(nil), s.Pending...)
}

// putPending records p as what is held for its plugin, in place of any
// build held for it before. The approval and the decline are of what it
// declares, so a newer build declaring the same keeps them; the
// announcement is of the build, so a newer one is announced again.
func (s *InstallState) putPending(p Pending) {
	if s == nil {
		return
	}
	p.PreJail = p.PreJail || s.preJail(p.Key, p.Source)
	for i, q := range s.Pending {
		if q.Key == p.Key && q.Source == p.Source {
			same := sameDecl(q.Manifest, p.Manifest)
			p.Approved = q.Approved && same
			p.Declined = q.Declined && same
			p.Announced = q.Announced && q.Sha256 == p.Sha256 && q.Resolved == p.Resolved && q.Replaces == p.Replaces
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

// approved reports the user approved what rec declares for this plugin
// (this key from this source): an approval is of a declaration, not a
// build, so any build declaring the same is.
func (s *InstallState) approved(rec Installed) bool {
	if s == nil {
		return false
	}
	for _, q := range s.Pending {
		if q.Approved && q.Key == rec.Key && q.Source == rec.Source && sameDecl(q.Manifest, rec.Manifest) {
			return true
		}
	}
	return false
}

// sameDecl reports a and b declare the same.
func sameDecl(a, b Manifest) bool {
	return len(a.Widens(b)) == 0 && len(b.Widens(a)) == 0
}

// boundaryChange is what next declares beyond prev (adds) and what prev
// declared that next does not (removes), in the jail's words. Both empty:
// the same boundary.
func boundaryChange(prev, next Manifest) (adds, removes []string) {
	return prev.Widens(next), next.Widens(prev)
}

// changeText renders a boundary change for a log line or a notice.
func changeText(adds, removes []string) string {
	var parts []string
	if len(adds) > 0 {
		parts = append(parts, "adds: "+strings.Join(adds, "; "))
	}
	if len(removes) > 0 {
		parts = append(parts, "removes: "+strings.Join(removes, "; "))
	}
	return strings.Join(parts, "; ")
}

// logHeldUpdate logs an update held because it changes what the plugin may
// do.
func logHeldUpdate(opts Options, name, running string, rec Installed, adds, removes []string) {
	opts.logf("plugin %s: update %s -> %s HELD: it changes what the plugin may do — %s. %s keeps running as it is; `conductor plugin approve %s` accepts it",
		name, running, rec.Resolved, changeText(adds, removes), running, ApproveArg(name, rec))
}

// Fingerprint names what m declares: a short digest of the permission
// manifest, which `conductor plugin approve <name>@<fingerprint>` takes, so
// the user approves the declaration they were shown and not one held after
// they looked.
func Fingerprint(m Manifest) string {
	b, _ := yaml.Marshal(m)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// MarkAnnounced records that the held build p was announced, unless another
// build has been held for that plugin since.
func (s *InstallState) MarkAnnounced(p Pending) {
	if s == nil {
		return
	}
	for i, q := range s.Pending {
		if q.Key == p.Key && q.Source == p.Source && q.Sha256 == p.Sha256 && q.Resolved == p.Resolved {
			s.Pending[i].Announced = true
		}
	}
}

// HeldFor is the update waiting for the user for the plugin with that
// install key from that source, if any.
func (s *InstallState) HeldFor(key, source string) (Pending, bool) {
	if s == nil {
		return Pending{}, false
	}
	for _, q := range s.Pending {
		if q.Key == key && q.Source == source && q.Waiting() {
			return q, true
		}
	}
	return Pending{}, false
}

// Message is the one-line notice of a held update: the plugin, the running
// and held versions, what it adds and removes, and how to approve it.
func (p Pending) Message() string {
	return fmt.Sprintf("plugin %s: update %s -> %s is waiting for your approval because it changes what the plugin may do — %s. %s keeps running as it is. Review it on the dashboard's Plugins screen; `conductor plugin approve %s` applies it, `conductor plugin decline %s` keeps %s.",
		p.Name, p.Replaces, p.Resolved, changeText(p.Widens, p.Removes), p.Replaces, ApproveArg(p.Name, p.Installed), p.Name, p.Replaces)
}

// ApproveArg is what `conductor plugin approve` takes to approve what rec,
// held for the plugin named name, declares: the name and the declaration's
// Fingerprint, so the approval is of what the user was shown.
func ApproveArg(name string, rec Installed) string {
	return name + "@" + Fingerprint(rec.Manifest)
}

// Approve approves what is held for the plugin(s) arg names: "<name>" (its
// name or its install key) approves what is held for it now; "<name>@<what>"
// only when <what> names it: the declaration's Fingerprint, or (as before)
// a prefix of the held build's sha (7 characters or more), its tag, or the
// tag's version. Each is installed as the current build (when its binary is
// still on disk; otherwise the approval is recorded and the next update pass
// installs any build declaring no more without holding). It returns what was
// approved.
func (s *InstallState) Approve(arg string) ([]Pending, error) {
	if s == nil {
		return nil, fmt.Errorf("no install state")
	}
	name, build, _ := strings.Cut(arg, "@")
	var waiting []string
	for _, q := range s.Pending {
		if (q.Name == name || q.Key == name) && !q.Approved {
			waiting = append(waiting, ApproveArg(q.Name, q.Installed)+" ("+q.Resolved+")")
		}
	}
	if len(waiting) == 0 {
		var names []string
		for _, q := range s.Pending {
			if !q.Approved {
				names = append(names, q.Name)
			}
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("plugin %s: nothing is waiting for approval", name)
		}
		return nil, fmt.Errorf("plugin %s: nothing is waiting for approval (waiting: %s)", name, strings.Join(names, ", "))
	}
	var done []Pending
	kept := s.Pending[:0]
	for _, q := range s.Pending {
		if (q.Name != name && q.Key != name) || q.Approved || (build != "" && !isBuild(q.Installed, build)) {
			kept = append(kept, q)
			continue
		}
		q.Approved, q.Declined = true, false
		done = append(done, q)
		if binaryPresent(q.Path) {
			// Installed: its record is what later builds compare with.
			s.Put(q.Installed)
			continue
		}
		kept = append(kept, q)
	}
	s.Pending = kept
	if len(done) == 0 {
		return nil, fmt.Errorf("plugin %s: %s is not what is waiting for approval — that is %s; review it with `conductor plugin approve`", name, build, strings.Join(waiting, ", "))
	}
	return done, nil
}

// Decline keeps the installed build of the plugin named name (its name or
// install key) instead of what is held for it: the held build is not
// installed, and that declaration is not asked about again (a newer build
// declaring something else is). It returns what was declined.
func (s *InstallState) Decline(name string) ([]Pending, error) {
	if s == nil {
		return nil, fmt.Errorf("no install state")
	}
	var done []Pending
	for i, q := range s.Pending {
		if (q.Name == name || q.Key == name) && q.Waiting() {
			s.Pending[i].Declined = true
			done = append(done, s.Pending[i])
		}
	}
	if len(done) == 0 {
		return nil, fmt.Errorf("plugin %s: no update is waiting for approval", name)
	}
	return done, nil
}

// isBuild: what names rec (Approve): its declaration's Fingerprint, its
// tag or version, or a prefix of its sha.
func isBuild(rec Installed, what string) bool {
	if what == Fingerprint(rec.Manifest) || what == rec.Resolved || what == path.Base(rec.Resolved) {
		return true
	}
	return len(what) >= 7 && strings.HasPrefix(rec.Sha256, strings.ToLower(what))
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
