package config

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConnBinding is one `packs.<inst>.connectors:` entry: a single connector
// instance name (the common case, and the only form a single socket takes),
// or a LIST of them for a socket the pack declared `many: true`.
//
//	connectors:
//	  github: gh                      # one
//	  targets: [gcal-work, gcal-side] # a list socket
//
// A list is the consumer naming, connector by connector, what the pack may
// reach. There is deliberately no pattern form: a pattern would let a
// connector added later join the pack's capability set without anyone
// naming it.
type ConnBinding struct {
	One  string
	List []string
	// isList records the YAML shape, so `[x]` (a list of one) and `x` are
	// told apart for the error messages, and a list round-trips as a list.
	isList bool
}

// Single returns a scalar binding.
func Single(name string) ConnBinding { return ConnBinding{One: name} }

// ListOf returns a list binding.
func ListOf(names ...string) ConnBinding { return ConnBinding{List: names, isList: true} }

// IsList reports whether the consumer wrote a list.
func (b ConnBinding) IsList() bool { return b.isList }

// Names is every connector this binding names, in the order written.
func (b ConnBinding) Names() []string {
	if b.isList {
		return b.List
	}
	if b.One == "" {
		return nil
	}
	return []string{b.One}
}

func (b *ConnBinding) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*b = ConnBinding{}
		return n.Decode(&b.One)
	case yaml.SequenceNode:
		var names []string
		if err := n.Decode(&names); err != nil {
			return fmt.Errorf("a connector list takes connector names: %w", err)
		}
		*b = ConnBinding{List: names, isList: true}
		return nil
	}
	return fmt.Errorf("a connector binding is a connector name, or a list of them for a list (many: true) socket")
}

func (b ConnBinding) MarshalYAML() (any, error) {
	if b.isList {
		return b.List, nil
	}
	return b.One, nil
}

// singleBindings reduces the instance's connector bindings to the scalar
// ones, which is all the 1:1 machinery (env.conn, source binding) handles.
// List bindings travel separately (envBindings.many).
func singleBindings(m map[string]ConnBinding) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if !v.isList {
			out[k] = v.One
		}
	}
	return out
}

func listBindings(m map[string]ConnBinding) map[string][]string {
	out := map[string][]string{}
	for k, v := range m {
		if v.isList {
			out[k] = append([]string(nil), v.List...)
		}
	}
	return out
}

func mergeListMaps(a, b map[string][]string) map[string][]string {
	out := map[string][]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// resolveListSockets moves every list socket's binding into env.many,
// validated: each declared `many:` socket is bound to known connectors of one
// type (the declared `type:` when given), with at least `min:` members and no
// repeats. A scalar binding of a list socket is a list of one; a list bound
// to a single socket is an error.
func (st *packInstantiation) resolveListSockets(ns string, reqs ConnectorReqs, env envBindings) error {
	for name, members := range env.many {
		req, ok := reqs[name]
		if !ok {
			continue // validateRequires/source checks report undeclared names
		}
		if !req.Many {
			return fmt.Errorf("pack %q: connectors: { %s: [%s] } is a list, but %q is a single-connector socket — bind one connector", ns, name, strings.Join(members, ", "), name)
		}
	}
	for _, name := range reqs.Names() {
		req := reqs[name]
		if !req.Many {
			continue
		}
		members, ok := env.many[name]
		if !ok {
			if one, isOne := env.conn[name]; isOne {
				members = []string{one}
				delete(env.conn, name)
			}
		}
		if len(members) == 0 {
			if !req.Required {
				st.warnf("pack %q: optional connector list %q is not bound — the parts of the pack that use it are DORMANT. Bind it with connectors: { %s: [<connector>, …] }.", ns, name, name)
				continue
			}
			return fmt.Errorf("pack %q: requires a list of connectors for %q — bind it: connectors: { %s: [<connector>, …] } (a list socket is never auto-bound: which connectors the pack may reach is yours to name)", ns, name, name)
		}
		min := req.Min
		if min < 1 {
			min = 1
		}
		if len(members) < min {
			return fmt.Errorf("pack %q: connectors.%s binds %d connector(s); the pack needs at least %d", ns, name, len(members), min)
		}
		seen := map[string]bool{}
		typ := req.Type
		for _, m := range members {
			if seen[m] {
				return fmt.Errorf("pack %q: connectors.%s lists %q twice", ns, name, m)
			}
			seen[m] = true
			ref, ok := st.cfg.ConnectorsMap[m]
			if !ok {
				return fmt.Errorf("pack %q: connectors.%s names %q, which is no connector in your config (defined: %s)", ns, name, m, connectorNames(st.cfg))
			}
			got := connectorType(ref)
			switch {
			case typ == "":
				typ = got
			case got != typ:
				return fmt.Errorf("pack %q: connectors.%s: %q is a %s connector; every member must be %s", ns, name, m, got, typ)
			}
		}
		env.many[name] = members
	}
	return nil
}

// checkListVersions gates every member of every list socket against the
// socket's constraint, exactly as a single socket's one connector is gated.
func (st *packInstantiation) checkListVersions(ns string, reqs ConnectorReqs, env envBindings) error {
	for _, name := range reqs.Names() {
		req := reqs[name]
		if !req.Many {
			continue
		}
		for _, m := range env.many[name] {
			one := envBindings{conn: map[string]string{name: m}}
			if err := st.checkConnectorVersions(ns, ConnectorReqs{name: {Version: req.Version, Required: true}}, one); err != nil {
				return err
			}
		}
	}
	return nil
}

// bindListStep points a step that names a list socket at that socket's
// members: `uses: targets.events` becomes `uses: <first member>.events` with
// Members set to the bound list. The step must say which member it means —
// `over: true` or `member:` — and a pack may not write `members:` itself
// (the list is the consumer's, never the pack's).
func (rw *refRewriter) bindListStep(s *Step) error {
	if s.Uses == "" {
		if s.Over || s.Member != "" {
			return fmt.Errorf("`over:`/`member:` need a verb step (`uses: <list socket>.<verb>`)")
		}
		return nil
	}
	conn, verb, _ := strings.Cut(s.Uses, ".")
	members, isList := rw.env.many[conn]
	if !isList {
		if _, declared := rw.listSockets[conn]; declared {
			// An optional list socket left unbound: dormant, like an
			// unbound optional single connector.
			return nil
		}
		if s.Over || s.Member != "" {
			return fmt.Errorf("`uses: %s` with over:/member: — %q is not a list (many: true) socket", s.Uses, conn)
		}
		return nil
	}
	if !s.Over && s.Member == "" {
		return fmt.Errorf("`uses: %s` names list socket %q — say which member: `over: true` (each one) or `member: <name>` (one, e.g. \"{{.item.target}}\")", s.Uses, conn)
	}
	if s.Over && s.Member != "" {
		return fmt.Errorf("`uses: %s`: over: and member: are exclusive", s.Uses)
	}
	if s.Over && s.ForEach != "" {
		return fmt.Errorf("`uses: %s`: over: already fans out over the members; drop for_each (or use member: inside the for_each)", s.Uses)
	}
	s.Members = append([]string(nil), members...)
	s.Uses = members[0] + "." + verb
	return nil
}

// rebindListSource expands a trigger's `on: <list socket>.<event>` into one
// source per member, so the trigger fires for an event from any of them. The
// scalar form becomes the list form; the per-source block (filter, policy,
// hooks) is copied to every member.
func (rw *refRewriter) expandListSources(t *TriggerSpec) {
	if t.On != "" {
		conn, ev, _ := strings.Cut(t.On, ".")
		if members, ok := rw.env.many[conn]; ok {
			t.OnSources = nil
			for _, m := range members {
				t.OnSources = append(t.OnSources, OnSource{Source: m + "." + ev})
			}
			t.On = ""
		}
		return
	}
	var out []OnSource
	for _, src := range t.OnSources {
		conn, ev, _ := strings.Cut(src.Source, ".")
		members, ok := rw.env.many[conn]
		if !ok {
			out = append(out, src)
			continue
		}
		for _, m := range members {
			c := src
			c.Source = m + "." + ev
			out = append(out, c)
		}
	}
	t.OnSources = out
}

// listSocketNames are the manifest's declared list sockets.
func listSocketNames(reqs ConnectorReqs) map[string]bool {
	out := map[string]bool{}
	for n, r := range reqs {
		if r.Many {
			out[n] = true
		}
	}
	return out
}

// sortedMembers is for stable messages.
func sortedMembers(m []string) []string {
	out := append([]string(nil), m...)
	sort.Strings(out)
	return out
}
