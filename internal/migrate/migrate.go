// Package migrate transforms a legacy conductor config (integrations: /
// notify: / handoffs: / controllers: / control: / paseo_bin) into the
// connectors-model schema (connectors: / runtimes: / triggers: / policy:).
//
// The transform is total or it refuses: every legacy construct maps, and
// anything unmappable is a hard error naming exactly what didn't map — never
// a quiet loss. Fields that were decoded but never read by the legacy engine
// (documented inert fields) are dropped WITH a summary note.
//
// It operates on the RAW yaml — no environment expansion — so ${VAR} secret
// references survive verbatim into the output. Blocks that carry through
// unchanged (agents:, notify:, store:, update:, …) are lifted as their
// original yaml nodes, preserving formatting and comments.
package migrate

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
)

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
func Transform(raw []byte) (*Result, error) {
	raw = maskEnv(raw)
	var cfg config.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w (note: config migrate reads the raw file — a ${VAR} in a numeric field can't be parsed; quote or inline it)", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if !isLegacy(&cfg) {
		return &Result{Changed: false}, nil
	}
	if cfg.HasConnectors() {
		return nil, fmt.Errorf("config already has connectors:/triggers: blocks alongside legacy ones — finish the migration by hand (mixed files are valid to RUN, but the automatic transform only handles fully-legacy files)")
	}

	var notes []string
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
		case "sentry":
			conn, trs, err = sentryTransform(ref.Name, ref, &notes)
		case "pagerduty":
			conn, trs, err = pagerdutyTransform(ref.Name, ref, &notes)
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
			Type: cc.Type, Agent: cc.Agent, Transport: cc.Transport,
			SessionModel: cc.SessionModel, Default: cc.Default,
			Tool: cc.Tool, Command: cc.Command,
		}
		notes = append(notes, fmt.Sprintf("controllers.%s → runtimes.%s", cname, cname))
	}
	if cfg.PaseoBin != "" && cfg.PaseoBin != "paseo" {
		if patched := patchPaseoBin(runtimes, cfg.PaseoBin); patched != "" {
			notes = append(notes, fmt.Sprintf("paseo_bin → runtimes.%s.bin", patched))
		} else {
			runtimes["paseo"] = config.RuntimeConfig{Type: "paseo", Bin: cfg.PaseoBin}
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

	// notify: sinks → connectors + via routes (the verb-layer delivery, with
	// byte-identical payloads); on/push/digest stay on the notify block.
	notifyNode, err := notifySinksToVia(&cfg, connectors, &notes)
	if err != nil {
		return nil, err
	}

	// Assemble the output document: transformed blocks are new nodes; carried
	// blocks are the original nodes (comments preserved); legacy-only keys
	// are dropped (their content lives in the new blocks).
	dropped := map[string]bool{
		"integrations": true, "control": true, "handoff": true,
		"handoffs": true, "controllers": true, "paseo_bin": true,
	}
	if notifyNode != nil {
		dropped["notify"] = true
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
	// agents: carried verbatim below; controller: references stay valid (the
	// new schema accepts both controller: and runtime: on a profile).
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
	if notifyNode != nil {
		if err := out.set("notify", notifyNode); err != nil {
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
	// The transform must produce a parseable document (belt and braces before
	// the caller's full validation). Checked BEFORE unmasking: after ${VAR}
	// references are restored, parseability depends on the environment again.
	var check config.Config
	if err := yaml.Unmarshal(b, &check); err != nil {
		return nil, fmt.Errorf("transformed config does not re-parse: %w", err)
	}
	return &Result{Output: unmaskEnv(b), Summary: notes, Changed: true}, nil
}

// notifySinksToVia maps the legacy notify sink fields onto connectors + via
// routes. Payloads are byte-identical to the legacy posters: slack/discord/
// pushover carried a "conductor " prefix on the line, ntfy carried it in the
// Title header, notifiarr in the notification name. Returns nil when no sink
// is configured (the notify block then carries through verbatim).
func notifySinksToVia(cfg *config.Config, connectors map[string]map[string]any, notes *[]string) (map[string]any, error) {
	n := cfg.Notify
	hasSinks := n.SlackWebhookURL != "" || n.DiscordWebhookURL != "" || n.Ntfy.Topic != "" ||
		(n.Pushover.Token != "" && n.Pushover.User != "") || n.Notifiarr.APIKey != ""
	if !hasSinks {
		return nil, nil
	}
	addConn := func(name string, conn map[string]any) error {
		if _, dup := connectors[name]; dup {
			return fmt.Errorf("notify: generated connector name %q collides with an existing integration — rename that integration and re-run", name)
		}
		connectors[name] = conn
		return nil
	}
	var via []map[string]any
	if n.SlackWebhookURL != "" {
		if err := addConn("notify-slack", map[string]any{"type": "slack", "webhook_url": n.SlackWebhookURL}); err != nil {
			return nil, err
		}
		via = append(via, map[string]any{
			"uses": "notify-slack.post", "options": map[string]any{"text": "conductor {{.message}}"},
		})
		*notes = append(*notes, "notify.slack_webhook_url → connector notify-slack + via route")
	}
	if n.DiscordWebhookURL != "" {
		if err := addConn("notify-discord", map[string]any{"type": "discord", "webhook_url": n.DiscordWebhookURL}); err != nil {
			return nil, err
		}
		via = append(via, map[string]any{
			"uses": "notify-discord.post", "options": map[string]any{"text": "conductor {{.message}}"},
		})
		*notes = append(*notes, "notify.discord_webhook_url → connector notify-discord + via route")
	}
	if n.Ntfy.Topic != "" {
		conn := map[string]any{"type": "ntfy", "topic": n.Ntfy.Topic}
		if n.Ntfy.Server != "" {
			conn["server"] = n.Ntfy.Server
		}
		if err := addConn("notify-ntfy", conn); err != nil {
			return nil, err
		}
		via = append(via, map[string]any{
			"uses": "notify-ntfy.publish", "options": map[string]any{"title": "conductor", "message": "{{.message}}"},
		})
		*notes = append(*notes, "notify.ntfy → connector notify-ntfy + via route")
	}
	if n.Pushover.Token != "" && n.Pushover.User != "" {
		if err := addConn("notify-pushover", map[string]any{"type": "pushover", "token": n.Pushover.Token, "user": n.Pushover.User}); err != nil {
			return nil, err
		}
		via = append(via, map[string]any{
			"uses": "notify-pushover.notify", "options": map[string]any{"message": "conductor {{.message}}"},
		})
		*notes = append(*notes, "notify.pushover → connector notify-pushover + via route")
	}
	if n.Notifiarr.APIKey != "" {
		conn := map[string]any{"type": "notifiarr", "api_key": n.Notifiarr.APIKey}
		if n.Notifiarr.ChannelID != "" {
			conn["channel_id"] = n.Notifiarr.ChannelID
		}
		if err := addConn("notify-notifiarr", conn); err != nil {
			return nil, err
		}
		via = append(via, map[string]any{
			"uses": "notify-notifiarr.notify", "options": map[string]any{"text": "{{.message}}"},
		})
		*notes = append(*notes, "notify.notifiarr → connector notify-notifiarr + via route")
	}
	out := map[string]any{"via": via}
	if len(n.On) > 0 {
		out["on"] = strSlice(n.On)
	}
	if n.Push {
		out["push"] = true
	}
	if n.Digest != 0 {
		out["digest"] = n.Digest.String()
	}
	return out, nil
}

// isLegacy reports whether the document carries legacy constructs.
func isLegacy(c *config.Config) bool {
	return len(c.Integrations) > 0 || len(c.Handoffs) > 0 ||
		c.Handoff.Web.BaseURL != "" || c.Handoff.Web.Listen != "" ||
		len(c.Controllers) > 0 || (c.PaseoBin != "" && c.PaseoBin != "paseo") ||
		controlPolicy(c.Control) != nil || legacyNotifySinks(c.Notify)
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

// patchPaseoBin sets bin on an existing paseo-type runtime; returns its name
// or "".
func patchPaseoBin(runtimes map[string]config.RuntimeConfig, bin string) string {
	for name, rt := range runtimes {
		if rt.Type == "paseo" {
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
