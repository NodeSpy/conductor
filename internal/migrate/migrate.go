// Package migrate transforms a legacy conductor config (integrations: /
// notify: / handoffs: / controllers: / control: / paseo_bin) into the
// connectors-model schema (connectors: / runtimes: / triggers: / policy:).
//
// Every legacy CONSTRUCT maps or the transform refuses naming it (an
// unmappable integration type, nested steps, control.enabled: false). Legacy
// KEYS the schema no longer knows — retired options, inert fields, blocks
// from configs that predate the schema — are DROPPED with a summary note
// instead: the migration is one-time, and it must produce a loadable
// connectors config from any legacy file (a key refusal on a deployed box
// would crash-loop it on auto-update). The output is checked against the
// strict runtime decode before it is returned.
//
// It operates on the RAW yaml — no environment expansion — so ${VAR} secret
// references survive verbatim into the output. Blocks that carry through
// unchanged (notify:, store:, update:, …) are lifted as their original yaml
// nodes, preserving formatting and comments.
package migrate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
)

// decodeConfig decodes raw YAML into a Config with anchors resolved first.
//
// Every pass that decodes bytes needs this: a custom UnmarshalYAML
// re-encodes the node it is handed, so an alias whose anchor lives outside
// that node cannot be read. The migration's own output carries anchors, so
// without this a second run — which must be a no-op — is a hard error.
// Callers keep the ORIGINAL bytes for the node tree they rewrite, so
// anchors, comments, and formatting survive.
func decodeConfig(b []byte, out *config.Config) error {
	if resolved, err := config.ResolveAliasBytes(b); err == nil {
		b = resolved
	}
	return yaml.Unmarshal(b, out)
}

// Result is one file's transform outcome.
type Result struct {
	// Output is the transformed YAML (nil when Changed is false).
	Output []byte
	// Summary lists every mapping decision worth a human's eye.
	Summary []string
	// Changed reports whether the file had legacy constructs to transform.
	Changed bool
}

// envTokenRe matches ${VAR} references; maskEnvRe reverses the masking.
var (
	envTokenRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	maskEnvRe  = regexp.MustCompile(`__CONDUCTOR_ENV__([A-Za-z0-9_]+)__`)
)

// maskEnv rewrites ${VAR} to a plain-scalar-safe token so the raw file parses
// WITHOUT expansion (a bare ${VAR} inside a YAML flow mapping is not valid
// YAML — the legacy loader only parses it because expandEnv ran first).
// unmaskEnv restores the references in the output.
func maskEnv(raw []byte) []byte {
	return envTokenRe.ReplaceAll(raw, []byte("__CONDUCTOR_ENV__${1}__"))
}

func unmaskEnv(out []byte) []byte {
	return maskEnvRe.ReplaceAll(out, []byte("${${1}}"))
}

// Transform converts one legacy config document. The raw bytes must be the
// on-disk file (unexpanded); the output preserves ${VAR} references.
func Transform(raw []byte) (*Result, error) { return TransformWith(raw, nil) }

// TransformWith is Transform given the agent profiles declared ELSEWHERE in
// the import tree. `agents:` commonly sat in the main config while the
// triggers naming it sat in conf.d/*.yaml, and profile behavior is inlined
// at each site now rather than parked in a registry — so a file holding only
// triggers needs the table to inline from. AutoMigrate gathers it with
// CollectProfiles before rewriting any file; a single-file caller passes nil
// and gets the file's own profiles only.
func TransformWith(raw []byte, profiles map[string]*yaml.Node) (*Result, error) {
	raw = maskEnv(raw)
	// A file this migration has ALREADY produced carries anchors, and a
	// strict/lenient decode of raw bytes cannot read those (a custom
	// UnmarshalYAML re-encodes the node it is handed, and an alias whose
	// anchor lives outside that node has nothing to point at). Decode from
	// an alias-resolved rendering; the NODE tree below stays on the
	// original bytes so a rewrite preserves the anchors, the comments, and
	// the formatting. Without this, re-running the migration on its own
	// output is a hard error instead of the no-op it must be.
	flat := raw
	if resolved, err := config.ResolveAliasBytes(raw); err == nil {
		flat = resolved
	}
	var notes []string
	droppedSeen := map[string]bool{}
	// LENIENT decode, with a strict probe harvesting notes: this migration is
	// one-time, and it must produce a loadable connectors config from ANY
	// legacy file. A key the schema doesn't know (a retired option like
	// notify.comment_on_escalate, a long-dead top-level dispatch: block) is
	// DROPPED with a note naming it — a hard refusal here means a deployed
	// box crash-loops on auto-update, which is strictly worse than losing a
	// field the legacy engine never read. The strict-output scrub at the end
	// guarantees anything carried verbatim is gone from the result too.
	{
		dec := yaml.NewDecoder(bytes.NewReader(flat))
		dec.KnownFields(true)
		var probe config.Config
		if err := dec.Decode(&probe); err != nil && err != io.EOF {
			if fes, ok := unknownFields(err); ok {
				for _, fe := range fes {
					noteUnknown(&notes, droppedSeen, fe)
				}
			}
			// A genuine parse error (not unknown keys) surfaces below.
		}
	}
	var cfg config.Config
	if err := yaml.Unmarshal(flat, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w (note: config migrate reads the raw file — a ${VAR} in a numeric field can't be parsed; quote or inline it)", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if !isLegacy(&cfg) {
		// A connectors-schema file may still carry retired models — a
		// notify: block (→ conductor.* triggers) and/or pre-vaults secret
		// refs — the standalone passes rewrite them; an already-migrated
		// file changes nothing. (Pre-pass notes are discarded on the
		// unchanged path: a current-schema file's unknown keys are the
		// strict runtime loader's business, not the migration's.)
		notes = nil
		cur, anyChanged := raw, false
		if out, changed, err := applyNotifyPass(cur, &notes); err != nil {
			return nil, fmt.Errorf("notify migration: %w", err)
		} else if changed {
			cur, anyChanged = out, true
		}
		if out, changed, err := applyVaultsPass(cur, &notes); err != nil {
			return nil, fmt.Errorf("vaults migration: %w", err)
		} else if changed {
			cur, anyChanged = out, true
		}
		if out, changed, err := applyAgentGuidancePass(cur, &notes); err != nil {
			return nil, fmt.Errorf("agent_guidance migration: %w", err)
		} else if changed {
			cur, anyChanged = out, true
		}
		// The use: pass runs LAST: it consumes the connectors:/runtimes: blocks
		// the passes above may have produced, and folds plugins:/type:/source:/
		// kind: into the single use: field.
		if out, changed, err := applyUsePass(cur, &notes); err != nil {
			return nil, fmt.Errorf("use migration: %w", err)
		} else if changed {
			cur, anyChanged = out, true
		}
		// The agents: pass runs after use:, so a budget it moves lands on a
		// runtimes: entry that already carries its `use:`.
		if out, changed, err := applyAgentsPass(cur, profiles, &notes); err != nil {
			return nil, fmt.Errorf("agents migration: %w", err)
		} else if changed {
			cur, anyChanged = out, true
		}
		if !anyChanged {
			return &Result{Changed: false}, nil
		}
		cur, err := scrubUnknownKeys(cur, &notes, droppedSeen)
		if err != nil {
			return nil, err
		}
		return &Result{Output: unmaskEnv(cur), Summary: notes, Changed: true}, nil
	}
	if cfg.HasConnectors() {
		return nil, fmt.Errorf("config already has connectors:/triggers: blocks alongside legacy ones — finish the migration by hand (mixed files are valid to RUN, but the automatic transform only handles fully-legacy files)")
	}

	out := newOutDoc()

	// integrations: → connectors: + triggers:.
	connectors := map[string]map[string]any{}
	var triggers []config.TriggerSpec
	for i, ref := range cfg.Integrations {
		if ref.Name == "" || ref.Type == "" {
			return nil, fmt.Errorf("integrations[%d]: missing name/type — cannot migrate a split/merged integration entry; migrate its file by hand", i)
		}
		var (
			conn map[string]any
			trs  []config.TriggerSpec
			err  error
		)
		switch ref.Type {
		case "github":
			conn, trs, err = githubTransform(ref.Name, ref, &notes)
		case "slack":
			conn, trs, err = slackTransform(ref.Name, ref, &notes)
		case "cron":
			conn, trs, err = cronTransform(ref.Name, ref, &notes)
		case "webhook":
			conn, trs, err = webhookTransform(ref.Name, ref, &notes)
		case "sentry", "pagerduty":
			// Extracted to an external plugin — recognised, deliberately not
			// transformed. See legacy_extracted.go for why an automatic
			// transform would emit a config that never fires.
			notes = append(notes, extractedNote(ref.Type, ref.Name))
			continue
		case "rss":
			conn, trs, err = rssTransform(ref.Name, ref, &notes)
		default:
			return nil, fmt.Errorf("integrations[%d]: unknown type %q — nothing to map it to; migrate by hand", i, ref.Type)
		}
		if err != nil {
			return nil, err
		}
		if !ref.IsEnabled() {
			conn["enabled"] = false
			notes = append(notes, fmt.Sprintf("%s[%s]: was disabled — connector carries enabled: false", ref.Type, ref.Name))
		}
		if _, dup := connectors[ref.Name]; dup {
			return nil, fmt.Errorf("duplicate integration name %q", ref.Name)
		}
		connectors[ref.Name] = conn
		triggers = append(triggers, trs...)
		notes = append(notes, fmt.Sprintf("%s[%s] → connector %q + %d trigger(s)", ref.Type, ref.Name, ref.Name, len(trs)))
	}

	// handoffs: / legacy handoff: → ask-capable connectors. Steps referencing
	// them by handoff: name keep working (the name now resolves to the
	// connector). The default entry's name is stamped onto background steps
	// that named none.
	defaultHandoff := cfg.DefaultHandoffName()
	handoffs := cfg.Handoffs
	if len(handoffs) == 0 && (cfg.Handoff.Web.BaseURL != "" || cfg.Handoff.Web.Listen != "") {
		web := cfg.Handoff.Web
		handoffs = map[string]config.HandoffConfig{"default": {Web: &web, Default: true}}
		defaultHandoff = "default"
	}
	hnames := make([]string, 0, len(handoffs))
	for n := range handoffs {
		hnames = append(hnames, n)
	}
	sort.Strings(hnames)
	for _, hname := range hnames {
		hc := handoffs[hname]
		conn, err := handoffConnector(hname, hc, connectors)
		if err != nil {
			return nil, err
		}
		connectors[hname] = conn
		notes = append(notes, fmt.Sprintf("handoffs.%s → connector %q (%s, ask)", hname, hname, conn["type"]))
	}
	if defaultHandoff != "" {
		stampDefaultHandoff(triggers, defaultHandoff)
		notes = append(notes, fmt.Sprintf("background steps with no handoff: now name the default explicitly (%s)", defaultHandoff))
	}

	// controllers: → runtimes: (same shape); paseo_bin → the paseo runtime's
	// bin.
	runtimes := map[string]config.RuntimeConfig{}
	for cname, cc := range cfg.Controllers {
		runtimes[cname] = config.RuntimeConfig{
			Use: runtimeUse(cc.Type, cc.Agent), Agent: cc.Agent, Transport: cc.Transport,
			SessionModel: cc.SessionModel, Default: cc.Default,
			Tool: cc.Tool, Command: cc.Command,
			// Bin and Host are load-bearing (.Controller() carries them): a
			// remote or custom-binary runtime must survive the migration.
			Bin: cc.Bin, Host: cc.Host,
		}
		notes = append(notes, fmt.Sprintf("controllers.%s → runtimes.%s", cname, cname))
	}
	if cfg.PaseoBin != "" && cfg.PaseoBin != "paseo" {
		if patched := patchPaseoBin(runtimes, cfg.PaseoBin); patched != "" {
			notes = append(notes, fmt.Sprintf("paseo_bin → runtimes.%s.bin", patched))
		} else {
			runtimes["paseo"] = config.RuntimeConfig{Use: "paseo", Bin: cfg.PaseoBin}
			notes = append(notes, "paseo_bin → runtimes.paseo.bin")
		}
	}

	// control: → policy: (global scope). An explicitly disabled box cannot
	// map — the connectors schema has no config kill switch (it is the runtime
	// `conductor pause`) — so refuse rather than silently re-enable it.
	if cfg.Control.Enabled != nil && !*cfg.Control.Enabled {
		return nil, fmt.Errorf("control.enabled: false has no connectors-schema equivalent (the global kill switch is `conductor pause`) — pause the daemon or drop the key, then re-run the migration")
	}
	policy := controlPolicy(cfg.Control)
	if policy != nil {
		notes = append(notes, "control → policy (shadow/pause_label/concurrency)")
	}

	// notify: → conductor.* triggers whose steps are the sink verbs (the
	// generated connectors carry byte-identical wire payloads). The block
	// itself is retired.
	notifyTriggers, err := notifyToTriggers(&cfg, connectors, &notes)
	if err != nil {
		return nil, err
	}
	triggers = append(triggers, notifyTriggers...)

	// Assemble the output document: transformed blocks are new nodes; carried
	// blocks are the original nodes (comments preserved); legacy-only keys
	// are dropped (their content lives in the new blocks).
	dropped := map[string]bool{
		"integrations": true, "control": true, "handoff": true,
		"handoffs": true, "controllers": true, "paseo_bin": true,
		"notify": true, // retired: alerting is conductor.* triggers now
	}
	if err := out.carryFrom(&doc, dropped, []string{"imports"}); err != nil {
		return nil, err
	}
	if len(connectors) > 0 {
		if err := out.set("connectors", connectors); err != nil {
			return nil, err
		}
	}
	if len(runtimes) > 0 {
		if err := out.set("runtimes", runtimes); err != nil {
			return nil, err
		}
	}
	// agents: is carried verbatim below and then decomposed into steps:
	// templates by applyAgentsPass (which runs over this output).
	if len(triggers) > 0 {
		if err := out.set("triggers", triggers); err != nil {
			return nil, err
		}
	}
	if policy != nil {
		if err := out.set("policy", policy); err != nil {
			return nil, err
		}
	}
	if err := out.carryFrom(&doc, dropped, nil); err != nil {
		return nil, err
	}

	b, err := out.marshal()
	if err != nil {
		return nil, err
	}
	// The vaults pass runs over the legacy transform's output: legacy
	// credential fields carry their scheme URIs into the new schema, and
	// those must not survive migration.
	if vout, vchanged, verr := applyVaultsPass(b, &notes); verr != nil {
		return nil, fmt.Errorf("vaults migration: %w", verr)
	} else if vchanged {
		b = vout
	}
	// Same for the use: pass — the legacy transform emits connectors:/runtimes:
	// entries in the pre-`use:` shape, so it folds them the same way it folds a
	// hand-written connectors-schema file.
	if uout, uchanged, uerr := applyUsePass(b, &notes); uerr != nil {
		return nil, fmt.Errorf("use migration: %w", uerr)
	} else if uchanged {
		b = uout
	}
	// …and the agents: pass over that output: the legacy transform carries
	// agents: through verbatim, so it needs the same decomposition a
	// hand-written connectors config does.
	if aout, achanged, aerr := applyAgentsPass(b, profiles, &notes); aerr != nil {
		return nil, fmt.Errorf("agents migration: %w", aerr)
	} else if achanged {
		b = aout
	}
	// The transform must produce a document the STRICT runtime loader accepts
	// (belt and braces before the caller's full validation) — any key it
	// doesn't know (a retired legacy block carried verbatim) is scrubbed with
	// a note rather than left to crash-loop the box. Checked BEFORE
	// unmasking: after ${VAR} references are restored, parseability depends
	// on the environment again.
	b, err = scrubUnknownKeys(b, &notes, droppedSeen)
	if err != nil {
		return nil, err
	}
	return &Result{Output: unmaskEnv(b), Summary: notes, Changed: true}, nil
}

// ---------------------------------------------------------------------------
// Unknown-key leniency: harvest, note, scrub
// ---------------------------------------------------------------------------

// fieldNotFoundRe matches one strict-decode unknown-key entry. Anchored as a
// FULL match so a custom unmarshaler's wrapped error (whose inner line
// numbers point into a re-encoded node, not this document) can never
// masquerade as a scrubbable entry.
var fieldNotFoundRe = regexp.MustCompile(`^line (\d+): field (\S+) not found in type (\S+)$`)

type unknownField struct {
	line  int
	field string
	typ   string
}

// unknownFields extracts the unknown-key entries from a strict decode error.
// ok is true only when EVERY entry is a plain unknown-key entry — a mixed or
// genuine parse error is not safely scrubbable. An `ok` with no entries means
// the whole error was top-level `x-` holders, which are not unknown keys at
// all: the caller should treat the document as clean.
func unknownFields(err error) (fes []unknownField, ok bool) {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return nil, false
	}
	parsed := 0
	for _, e := range te.Errors {
		m := fieldNotFoundRe.FindStringSubmatch(e)
		if m == nil {
			return nil, false
		}
		parsed++
		line, _ := strconv.Atoi(m[1])
		fe := unknownField{line: line, field: m[2], typ: m[3]}
		// A top-level `x-` holder is an anchor park, not an unknown key —
		// the loader ignores it, so the migration must leave it in place
		// rather than scrub the anchors a user's config depends on.
		if fe.typ == "config.Config" && config.IsExtensionKey(fe.field) {
			continue
		}
		fes = append(fes, fe)
	}
	return fes, parsed > 0
}

// noteUnknown records one dropped key, once per (type, field).
func noteUnknown(notes *[]string, seen map[string]bool, fe unknownField) {
	// `agents:` left the schema but has a DEDICATED pass (applyAgentsPass)
	// that decomposes it and reports what it did. Reporting it here as well
	// would tell the operator their agents were dropped, which is the
	// opposite of what happens.
	if fe.field == "agents" && fe.typ == "config.Config" {
		return
	}
	key := fe.typ + "." + fe.field
	if seen[key] {
		return
	}
	seen[key] = true
	where := strings.TrimPrefix(fe.typ, "config.")
	*notes = append(*notes, fmt.Sprintf("dropped legacy key %q (%s): not part of the connectors schema — retired or never read; nothing carries over", fe.field, where))
}

// scrubUnknownKeys makes the transformed output strictly loadable: every key
// the connectors schema doesn't know (a retired block carried verbatim,
// e.g. a top-level dispatch: from an old backup) is removed with a note.
// Anything that isn't a plain unknown-key error stays a hard error.
func scrubUnknownKeys(b []byte, notes *[]string, seen map[string]bool) ([]byte, error) {
	// The output may carry ANCHORS — the agents pass emits one when several
	// steps in a file shared a profile. A strict decode cannot read those
	// directly (a custom UnmarshalYAML re-encodes the node it is handed, and
	// an alias whose anchor sits outside that node has nothing to point at),
	// so the check runs against an alias-resolved rendering.
	//
	// When it comes back clean the ORIGINAL is returned, anchors intact.
	// Only when something actually has to be scrubbed does the resolved
	// rendering become the output — inlining an anchor is a cosmetic loss,
	// and it beats refusing a migration over a document the loader would
	// have accepted.
	if resolved, err := config.ResolveAliasBytes(b); err == nil && !bytes.Equal(resolved, b) {
		dec := yaml.NewDecoder(bytes.NewReader(resolved))
		dec.KnownFields(true)
		var check config.Config
		err := dec.Decode(&check)
		if err == nil || err == io.EOF {
			return b, nil
		}
		// An error made up entirely of `x-` holders is not an error — those
		// are anchor parks the loader ignores.
		if fes, ok := unknownFields(err); ok && len(fes) == 0 {
			return b, nil
		}
		b = resolved
	}
	for pass := 0; pass < 20; pass++ {
		dec := yaml.NewDecoder(bytes.NewReader(b))
		dec.KnownFields(true)
		var check config.Config
		err := dec.Decode(&check)
		if err == nil || err == io.EOF {
			return b, nil
		}
		fes, ok := unknownFields(err)
		if !ok {
			return nil, fmt.Errorf("transformed config does not re-parse: %w", err)
		}
		if len(fes) == 0 {
			return b, nil // the only "unknowns" were x- holders
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return nil, err
		}
		removed := 0
		for _, fe := range fes {
			if removeKeyAt(&doc, fe.line, fe.field) {
				noteUnknown(notes, seen, fe)
				removed++
			}
		}
		if removed == 0 {
			// Located nothing — refuse rather than loop or guess.
			return nil, fmt.Errorf("transformed config does not re-parse: %w", err)
		}
		nb, err := marshalDoc(&doc)
		if err != nil {
			return nil, err
		}
		b = nb
	}
	return nil, fmt.Errorf("transformed config kept failing the strict re-parse after 20 scrub passes")
}

// removeKeyAt deletes the mapping entry whose KEY node named field sits at
// the given (1-based) line, anywhere in the tree.
func removeKeyAt(n *yaml.Node, line int, field string) bool {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Value == field && k.Line == line {
				n.Content = append(n.Content[:i], n.Content[i+2:]...)
				return true
			}
		}
	}
	for _, c := range n.Content {
		if removeKeyAt(c, line, field) {
			return true
		}
	}
	return false
}

// isLegacy reports whether the document carries legacy constructs.
func isLegacy(c *config.Config) bool {
	return len(c.Integrations) > 0 || len(c.Handoffs) > 0 ||
		c.Handoff.Web.BaseURL != "" || c.Handoff.Web.Listen != "" ||
		len(c.Controllers) > 0 || (c.PaseoBin != "" && c.PaseoBin != "paseo") ||
		controlPolicy(c.Control) != nil
}

// legacyNotifySinks reports whether the notify block still uses the legacy
// sink fields (they map onto connectors + via routes).
func legacyNotifySinks(n config.Notify) bool {
	return n.SlackWebhookURL != "" || n.DiscordWebhookURL != "" || n.Ntfy.Topic != "" ||
		(n.Pushover.Token != "" && n.Pushover.User != "") || n.Notifiarr.APIKey != ""
}

// handoffConnector maps one handoffs: entry to a connector map.
func handoffConnector(name string, hc config.HandoffConfig, existing map[string]map[string]any) (map[string]any, error) {
	if _, dup := existing[name]; dup {
		return nil, fmt.Errorf("handoffs.%s: name collides with integration %q — rename one and re-run", name, name)
	}
	switch {
	case hc.Web != nil:
		conn := map[string]any{"type": "web"}
		if hc.Web.BaseURL != "" {
			conn["base_url"] = hc.Web.BaseURL
		}
		if hc.Web.Listen != "" {
			conn["listen"] = hc.Web.Listen
		}
		if hc.Web.TTL != 0 {
			conn["ttl"] = hc.Web.TTL.String()
		}
		if hc.Web.Tunnel.Provider != "" || len(hc.Web.Tunnel.Command) > 0 {
			t := map[string]any{}
			tc := hc.Web.Tunnel
			if tc.Provider != "" {
				t["provider"] = tc.Provider
			}
			if tc.Host != "" {
				t["host"] = tc.Host
			}
			if tc.Mode != "" {
				t["mode"] = tc.Mode
			}
			if tc.SSHHost != "" {
				t["ssh_host"] = tc.SSHHost
			}
			if tc.Authtoken != "" {
				t["authtoken"] = tc.Authtoken
			}
			if tc.URLPattern != "" {
				t["url_pattern"] = tc.URLPattern
			}
			if len(tc.Command) > 0 {
				t["command"] = strSlice(tc.Command)
			}
			if tc.Account {
				t["account"] = true
			}
			conn["tunnel"] = t
		}
		return conn, nil
	case hc.Slack != nil:
		conn := map[string]any{"type": "slack", "bot_token": hc.Slack.BotToken}
		conn["options"] = chatAskOptions(hc.Slack)
		return conn, nil
	case hc.Discord != nil:
		conn := map[string]any{"type": "discord", "bot_token": hc.Discord.BotToken}
		conn["options"] = chatAskOptions(hc.Discord)
		return conn, nil
	}
	return nil, fmt.Errorf("handoffs.%s: sets none of web/slack/discord", name)
}

// chatAskOptions maps a chat hand-off's target into connector default
// options, which the ask verb (and background reviews) inherit.
func chatAskOptions(hc *config.HandoffChat) map[string]any {
	opts := map[string]any{"to": hc.To}
	if hc.User != "" {
		opts["user"] = hc.User
	}
	if hc.Channel != "" {
		opts["channel"] = hc.Channel
	}
	return opts
}

// stampDefaultHandoff sets the default hand-off name on background steps that
// named none (legacy resolution order made the default implicit).
func stampDefaultHandoff(triggers []config.TriggerSpec, def string) {
	for ti := range triggers {
		for si := range triggers[ti].Steps {
			s := &triggers[ti].Steps[si]
			if s.Background && s.Handoff == "" {
				s.Handoff = def
			}
		}
	}
}

// controlPolicy maps the legacy control: block to a global policy (nil when
// control was all defaults).
func controlPolicy(c config.Control) map[string]any {
	p := map[string]any{}
	// enabled is dropped: true is the default, and explicit false hard-errors
	// before this runs (no policy-level kill switch in the new schema).
	if c.Shadow {
		p["shadow"] = true
	}
	if c.PauseLabel != "" {
		p["pause_label"] = c.PauseLabel
	}
	conc := map[string]any{}
	if c.MaxConcurrentAgents != nil {
		conc["max_agents"] = *c.MaxConcurrentAgents
	}
	if c.MaxAgentsPerHour != 0 {
		conc["max_agents_per_hour"] = c.MaxAgentsPerHour
	}
	if len(conc) > 0 {
		p["concurrency"] = conc
	}
	if len(p) == 0 {
		return nil
	}
	return p
}

// runtimeUse maps a legacy controller's type:/agent: pair onto the single `use:`
// field. A controller naming an `agent:` is driven over ACP, which is now spelt
// `use: acp` with the agent alongside it. A controller naming neither (the
// implicit default) becomes the paseo runtime it always was.
func runtimeUse(ccType, ccAgent string) string {
	if ccType != "" {
		return ccType
	}
	if ccAgent != "" {
		return "acp"
	}
	return "paseo"
}

// patchPaseoBin sets bin on an existing paseo-type runtime; returns its name
// or "".
func patchPaseoBin(runtimes map[string]config.RuntimeConfig, bin string) string {
	for name, rt := range runtimes {
		if rt.Use == "paseo" {
			rt.Bin = bin
			runtimes[name] = rt
			return name
		}
	}
	return ""
}

// --- output document assembly (ordered, carrying original nodes) ---

// outDoc builds the output mapping with deliberate key order: carried header
// keys (imports), then the new blocks, then everything else in original
// order.
type outDoc struct {
	root    *yaml.Node
	present map[string]bool
}

func newOutDoc() *outDoc {
	return &outDoc{
		root:    &yaml.Node{Kind: yaml.MappingNode},
		present: map[string]bool{},
	}
}

// set marshals a Go value into a node and appends it under key.
func (o *outDoc) set(key string, v any) error {
	b, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("render %s: %w", key, err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("reparse %s: %w", key, err)
	}
	o.append(key, n.Content[0])
	return nil
}

// carryFrom lifts original top-level entries into the output: with only
// (non-nil) restricts to those keys; otherwise every key not dropped and not
// already present is carried in original order.
func (o *outDoc) carryFrom(doc *yaml.Node, dropped map[string]bool, only []string) error {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	m := doc.Content[0]
	if m.Kind != yaml.MappingNode {
		return fmt.Errorf("config is not a mapping")
	}
	onlySet := map[string]bool{}
	for _, k := range only {
		onlySet[k] = true
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i].Value
		if only != nil && !onlySet[k] {
			continue
		}
		if dropped[k] || o.present[k] {
			continue
		}
		o.append(k, m.Content[i+1])
	}
	return nil
}

func (o *outDoc) append(key string, val *yaml.Node) {
	if o.present[key] {
		return
	}
	o.present[key] = true
	o.root.Content = append(o.root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key}, val)
}

func (o *outDoc) marshal() ([]byte, error) {
	var b strings.Builder
	b.WriteString("# Migrated to the connectors model by `conductor config migrate`.\n")
	b.WriteString("# The original file was backed up alongside this one.\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(o.root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}
