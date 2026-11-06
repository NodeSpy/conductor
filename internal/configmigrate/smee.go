package configmigrate

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/configedit"
)

// The smee rule (retired in the release that moved smee out of core and out
// of the github plugin). A smee channel used to be dialed by whoever received
// the webhook: the github plugin (`webhook.smee_url`) and the bundled
// `webhook` connector (`smee_url`). It is now one exposure connector, the
// smee plugin, that relays the channel to a local listener through the
// generic `listeners` semantic. The rewrite, per connector:
//
//	gh:                                gh:
//	  use: github                        use: github
//	  webhook:                           webhook:
//	    smee_url: ${GH_SMEE_URL}           secret: ${GH_WEBHOOK_SECRET}
//	    secret: ${GH_WEBHOOK_SECRET}       listen: 127.0.0.1:8787
//	                                       path: /webhook
//	                                       expose: gh-smee
//	                                   gh-smee:
//	                                     use: smee
//	                                     channel: ${GH_SMEE_URL}
//
// The smee connector is pinned to the SAME channel, so whatever sends to it
// (a GitHub App's or repo's webhook) keeps working untouched. An existing
// listen address is kept (smee relays to it); a missing one gets a free
// loopback port. The github shape also gets its path written out (/webhook,
// the plugin's own default), because the engine's default for an unset
// listener path is "/". The bundled webhook connector's relay path stays its
// default ("/"), where a delivery is offered to every source as the smee
// channel did.
//
// A smee connector made for the bundled webhook connector also gets
// `trust: full` and `isolate: true`, with a comment saying why: the relay it
// replaces ran inside conductor, with conductor's trust, and the listener it
// delivers to is one of conductor's own ports, which a jailed plugin is never
// granted (docs/wiki/Plugins.md "What a plugin declares"). Isolated, its
// unjailed process is its own, so no other smee connector shares that trust.
// A github listener is a plugin's: its smee connector stays jailed, reaching
// the listener through expose_local.
//
// This is the only place left in core that knows the retired keys. It goes
// once no supported release can still be carrying them.

const (
	smeeUse         = "smee"
	githubSmeeKey   = "smee_url"
	githubPath      = "/webhook"
	firstListenPort = 8787
)

var smeeRule = rule{name: "smee", apply: applySmee}

func applySmee(t *tree) error {
	all := t.connectorNames()
	for _, s := range t.connectorSites() {
		body := docMap(s.body.doc, s.bodyPath)
		if wh, ok := body["webhook"].(map[string]any); ok {
			if url, ok := wh[githubSmeeKey].(string); ok && url != "" {
				if err := t.migrateSmee(s, all, append(clone(s.bodyPath), "webhook"), wh, url, true); err != nil {
					return err
				}
				continue
			}
		}
		if isBundledWebhook(body) {
			if url, ok := body["smee_url"].(string); ok && url != "" {
				if err := t.migrateSmee(s, all, clone(s.bodyPath), body, url, false); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// isBundledWebhook reports whether a connector body is the bundled generic
// webhook connector.
func isBundledWebhook(body map[string]any) bool {
	use, _ := body["use"].(string)
	typ, _ := body["type"].(string)
	return use == "webhook" || (use == "" && typ == "webhook")
}

// migrateSmee rewrites one connector: base is the map holding smee_url and
// the listener fields (`webhook` for the github shape, the connector body for
// the bundled webhook connector).
func (t *tree) migrateSmee(s connectorSite, all map[string]map[string]any, base []string, fields map[string]any, url string, github bool) error {
	where := s.name
	if github {
		where += ".webhook"
	}
	if exp, _ := fields["expose"].(string); exp != "" {
		t.note("%s: left as it is — smee_url and expose: %s are both set; one listener has one exposure, so pick one by hand (smee: add a connector `{use: smee, channel: <the url>}` and point expose at it)", where, exp)
		return nil
	}
	smeeName, reuse := t.smeeConnectorFor(s.name, url, all)
	listen, _ := fields["listen"].(string)
	newListen := ""
	if listen == "" {
		port, err := t.freePort()
		if err != nil {
			t.note("%s: left as it is — %v", where, err)
			return nil
		}
		newListen = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	}
	path, _ := fields["path"].(string)

	edit := func(d *configedit.Document, base []string) error {
		if newListen != "" {
			if err := d.SetScalar(append(clone(base), "listen"), newListen); err != nil {
				return err
			}
		}
		if github && path == "" {
			if err := d.SetScalar(append(clone(base), "path"), githubPath); err != nil {
				return err
			}
		}
		if err := d.SetScalar(append(clone(base), "expose"), smeeName); err != nil {
			return err
		}
		return d.Delete(append(clone(base), githubSmeeKey))
	}
	if err := t.editOrReplace(s.body, s.bodyPath, base, edit); err != nil {
		return fmt.Errorf("%s: %w — this connector needs a hand edit (see the smee connector docs)", where, err)
	}
	if !reuse {
		if err := t.addConnector(s.siblings, s.siblingPath, smeeName, url, !github); err != nil {
			return fmt.Errorf("%s: adding the smee connector %s: %w", where, smeeName, err)
		}
		all[smeeName] = map[string]any{"use": smeeUse, "channel": url}
		t.uses = append(t.uses, smeeUse)
	}
	t.ruleChanged = true
	t.touched = append(t.touched, s.name, smeeName)
	l := listen
	if l == "" {
		l = newListen
	}
	if reuse {
		t.note("%s: smee_url → expose: %s (the existing smee connector on the same channel), listening on %s", where, smeeName, l)
	} else if !github {
		t.note("%s: smee_url → a new smee connector %s on the same channel (use: %s, trust: full, isolate: true — the relay ran inside conductor, and this listener is conductor's own port), exposing the listener on %s", where, smeeName, smeeUse, l)
	} else {
		t.note("%s: smee_url → a new smee connector %s on the same channel (use: %s), exposing the listener on %s", where, smeeName, smeeUse, l)
	}
	return nil
}

// smeeConnectorFor names the smee connector for name's channel: an existing
// smee connector already pinned to it (reuse), else a fresh "<name>-smee".
func (t *tree) smeeConnectorFor(name, url string, all map[string]map[string]any) (string, bool) {
	for _, n := range sortedKeys(toAny(all)) {
		b := all[n]
		if use, _ := b["use"].(string); (use == smeeUse || strings.HasPrefix(use, smeeUse+"@")) && b["channel"] == url {
			return n, true
		}
	}
	cand := name + "-smee"
	for i := 2; ; i++ {
		if _, taken := all[cand]; !taken {
			return cand, false
		}
		cand = fmt.Sprintf("%s-smee%d", name, i)
	}
}

func toAny(m map[string]map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// editOrReplace applies edit to the field map at base in f. When the text
// can't take the edits in place (a flow-style `{...}` map), the smallest
// enclosing entry is rewritten whole from its decoded value instead — still
// only that entry; the rest of the file is untouched.
func (t *tree) editOrReplace(f *File, bodyPath, base []string, edit func(*configedit.Document, []string) error) error {
	snapshot := f.doc.Bytes()
	if err := edit(f.doc, base); err == nil {
		return nil
	}
	if err := t.reset(f, snapshot); err != nil {
		return err
	}
	// Rewrite the base map whole, then (if that's refused too) the
	// connector body whole.
	for _, at := range [][]string{base, bodyPath} {
		if len(at) == 0 {
			continue
		}
		cur, ok := f.doc.Get(at)
		if !ok {
			continue
		}
		scratch, err := configedit.Parse(mustYAML(map[string]any{"x": cur}))
		if err != nil {
			continue
		}
		rel := append([]string{"x"}, base[len(at):]...)
		if err := edit(scratch, rel); err != nil {
			continue
		}
		v, _ := scratch.Get([]string{"x"})
		if err := f.doc.SetScalar(at, v); err == nil {
			return nil
		}
		if err := t.reset(f, snapshot); err != nil {
			return err
		}
	}
	return fmt.Errorf("could not rewrite %s in place", strings.Join(base, "."))
}

func (t *tree) reset(f *File, src []byte) error {
	d, err := configedit.Parse(src)
	if err != nil {
		return err
	}
	f.doc = d
	return nil
}

// addConnector adds `name: {use: smee, channel: url}` to the connectors map
// at path in f (nil = the file's root, for a section file of bare entries);
// unjailed adds `trust: full` and `isolate: true` under trustComment.
func (t *tree) addConnector(f *File, path []string, name, url string, unjailed bool) error {
	snapshot := f.doc.Bytes()
	at := append(clone(path), name)
	keys := [][2]any{{"use", smeeUse}, {"channel", url}}
	if unjailed {
		keys = append(keys, [2]any{"trust", "full"}, [2]any{"isolate", true})
	}
	ok := true
	for _, kv := range keys {
		if err := f.doc.SetScalar(append(clone(at), kv[0].(string)), kv[1]); err != nil {
			ok = false
			break
		}
	}
	if !ok {
		if err := t.reset(f, snapshot); err != nil {
			return err
		}
		whole := map[string]any{"use": smeeUse, "channel": url}
		if unjailed {
			whole["trust"], whole["isolate"] = "full", true
		}
		if err := f.doc.SetScalar(at, whole); err != nil {
			return err
		}
	}
	if unjailed {
		return t.commentAbove(f, name, "trust", trustComment)
	}
	return nil
}

// trustComment says why a smee connector made for the bundled webhook
// connector runs unjailed.
const trustComment = "# trust/isolate: the smee_url relay this replaces ran inside conductor with its trust, and the webhook listener it delivers to is one of conductor's own ports, which a jailed plugin never reaches"

// commentAbove puts comment on its own line above the `key:` line of the
// connector entry name (the first such line after `name:`), at its indent.
// Best effort: a shape it does not find is left without the comment.
func (t *tree) commentAbove(f *File, name, key, comment string) error {
	lines := strings.SplitAfter(string(f.doc.Bytes()), "\n")
	entry := -1
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if entry < 0 {
			if trimmed == name+":" {
				entry = i
			}
			continue
		}
		if strings.HasPrefix(trimmed, key+":") {
			indent := l[:len(l)-len(strings.TrimLeft(l, " "))]
			out := strings.Join(lines[:i], "") + indent + comment + "\n" + strings.Join(lines[i:], "")
			return t.reset(f, []byte(out))
		}
	}
	return nil
}

// freePort picks a loopback port for a new listener: from 8787 up, one no
// listener in the tree uses and nothing holds right now.
func (t *tree) freePort() (int, error) {
	if t.takenPorts == nil {
		t.takenPorts = map[int]bool{}
		for _, f := range t.files {
			v, _ := f.doc.Value()
			collectListenPorts(v, t.takenPorts)
		}
	}
	for p := firstListenPort; p < firstListenPort+200; p++ {
		if t.takenPorts[p] || !t.portFree(p) {
			continue
		}
		t.takenPorts[p] = true
		return p, nil
	}
	return 0, fmt.Errorf("no free loopback port in %d-%d for its listener; set listen by hand", firstListenPort, firstListenPort+199)
}

// collectListenPorts records the port of every `listen` (or `*_listen`)
// address anywhere in v.
func collectListenPorts(v any, into map[int]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if s, ok := e.(string); ok && (k == "listen" || strings.HasSuffix(k, "_listen")) {
				if _, p, err := net.SplitHostPort(s); err == nil {
					if n, err := strconv.Atoi(p); err == nil {
						into[n] = true
					}
				}
			}
			collectListenPorts(e, into)
		}
	case []any:
		for _, e := range x {
			collectListenPorts(e, into)
		}
	}
}

// portFree probes whether a loopback port can be bound now.
func portFree(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func mustYAML(v any) []byte {
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
