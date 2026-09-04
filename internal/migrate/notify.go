package migrate

// The notify pass retires the notify: { on, via, sinks, digest } block:
// alerting becomes ordinary triggers on the conductor.* lifecycle source.
// Each configured sink maps to the same generated connector the earlier
// sink migration produced (byte-identical wire payloads); each enabled
// event becomes one trigger whose steps are the sink/via verbs. The legacy
// "escalate" policy covered BOTH give-ups and run errors, so it maps to
// conductor.escalate AND conductor.failed. The report-based digest timer
// maps onto the trigger grammar as a grouped completion digest
// (group: { window: <digest> }) — stated in the summary, never silent.

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
)

// conductorEventsFor maps one legacy notify event to conductor.* events.
func conductorEventsFor(ev string) []string {
	if ev == "escalate" {
		// The legacy escalate covered give-ups AND workflow errors; the
		// lifecycle splits them.
		return []string{"escalate", "failed"}
	}
	return []string{ev}
}

// notifySinkSteps builds the generated sink connectors plus one step per
// sink, with msg as the templated message ({{.message}} for the per-event
// triggers). Payloads match the legacy posters byte for byte.
func notifySinkSteps(n config.Notify, connectors map[string]map[string]any, msg string, notes *[]string) ([]config.Step, error) {
	addConn := func(name string, conn map[string]any) error {
		if existing, dup := connectors[name]; dup {
			if fmt.Sprint(existing) != fmt.Sprint(conn) {
				return fmt.Errorf("notify: generated connector name %q collides with an existing entry — rename it and re-run", name)
			}
			return nil // same generated connector (digest + event steps share it)
		}
		connectors[name] = conn
		return nil
	}
	var steps []config.Step
	if n.SlackWebhookURL != "" {
		if err := addConn("notify-slack", map[string]any{"type": "slack", "webhook_url": n.SlackWebhookURL}); err != nil {
			return nil, err
		}
		steps = append(steps, config.Step{Uses: "notify-slack.post",
			Options: map[string]any{"text": "conductor " + msg}})
		*notes = append(*notes, "notify.slack_webhook_url → connector notify-slack + trigger step")
	}
	if n.DiscordWebhookURL != "" {
		if err := addConn("notify-discord", map[string]any{"type": "discord", "webhook_url": n.DiscordWebhookURL}); err != nil {
			return nil, err
		}
		steps = append(steps, config.Step{Uses: "notify-discord.post",
			Options: map[string]any{"text": "conductor " + msg}})
		*notes = append(*notes, "notify.discord_webhook_url → connector notify-discord + trigger step")
	}
	if n.Ntfy.Topic != "" {
		conn := map[string]any{"type": "ntfy", "topic": n.Ntfy.Topic}
		if n.Ntfy.Server != "" {
			conn["server"] = n.Ntfy.Server
		}
		if err := addConn("notify-ntfy", conn); err != nil {
			return nil, err
		}
		steps = append(steps, config.Step{Uses: "notify-ntfy.publish",
			Options: map[string]any{"title": "conductor", "message": msg}})
		*notes = append(*notes, "notify.ntfy → connector notify-ntfy + trigger step")
	}
	if n.Pushover.Token != "" && n.Pushover.User != "" {
		if err := addConn("notify-pushover", map[string]any{"type": "pushover", "token": n.Pushover.Token, "user": n.Pushover.User}); err != nil {
			return nil, err
		}
		steps = append(steps, config.Step{Uses: "notify-pushover.notify",
			Options: map[string]any{"message": "conductor " + msg}})
		*notes = append(*notes, "notify.pushover → connector notify-pushover + trigger step")
	}
	if n.Notifiarr.APIKey != "" {
		conn := map[string]any{"type": "notifiarr", "api_key": n.Notifiarr.APIKey}
		if n.Notifiarr.ChannelID != "" {
			conn["channel_id"] = n.Notifiarr.ChannelID
		}
		if err := addConn("notify-notifiarr", conn); err != nil {
			return nil, err
		}
		steps = append(steps, config.Step{Uses: "notify-notifiarr.notify",
			Options: map[string]any{"text": msg}})
		*notes = append(*notes, "notify.notifiarr → connector notify-notifiarr + trigger step")
	}
	return steps, nil
}

// notifyConfigured reports whether the block carries anything to migrate.
func notifyConfigured(n config.Notify) bool { return n.Configured() }

// notifyToTriggers converts one notify block into conductor.* triggers plus
// the generated sink connectors. No configured behavior is dropped silently:
// an unmappable via route is a hard error, and inert pieces (push, an event
// list with no delivery) leave a summary note.
func notifyToTriggers(cfg *config.Config, connectors map[string]map[string]any, notes *[]string) ([]config.TriggerSpec, error) {
	n := cfg.Notify
	if !notifyConfigured(n) {
		return nil, nil
	}

	// event (legacy name) → the steps that fire for it.
	stepsFor := map[string][]config.Step{}
	sinkSteps, err := notifySinkSteps(n, connectors, "{{.message}}", notes)
	if err != nil {
		return nil, err
	}
	for _, ev := range n.On {
		stepsFor[ev] = append(stepsFor[ev], sinkSteps...)
	}
	if len(n.On) == 0 && len(sinkSteps) > 0 {
		*notes = append(*notes, "notify: sinks configured with no on: events — the legacy block delivered nothing; no per-event triggers generated")
	}
	for i, r := range n.Via {
		conn, verb, ok := strings.Cut(r.Uses, ".")
		if !ok || conn == "" || verb == "" {
			return nil, fmt.Errorf("notify.via[%d]: `uses: %s` is not <connector>.<verb> — migrate it by hand", i, r.Uses)
		}
		events := r.On
		if len(events) == 0 {
			events = n.On
		}
		if len(events) == 0 {
			*notes = append(*notes, fmt.Sprintf("notify.via[%d] (%s): no events enabled — the legacy block never fired it; dropped with this note", i, r.Uses))
			continue
		}
		for _, ev := range events {
			stepsFor[ev] = append(stepsFor[ev], config.Step{Uses: r.Uses, Options: r.Options})
		}
	}

	var out []config.TriggerSpec
	events := make([]string, 0, len(stepsFor))
	for ev := range stepsFor {
		events = append(events, ev)
	}
	sort.Strings(events)
	for _, ev := range events {
		if ev == "digest" {
			// The legacy digest event routed via-routes on the digest timer;
			// folded into the grouped digest trigger below.
			continue
		}
		for _, cev := range conductorEventsFor(ev) {
			out = append(out, config.TriggerSpec{
				Name:  "notify-" + cev,
				On:    "conductor." + cev,
				Steps: stepsFor[ev],
			})
		}
		*notes = append(*notes, fmt.Sprintf("notify.on %s → trigger(s) on conductor.%s", ev, strings.Join(conductorEventsFor(ev), " + conductor.")))
	}

	if n.Digest != 0 {
		msg := fmt.Sprintf("[digest] {{ len .group.events }} completed run(s) in the last %s", n.Digest.String())
		digestSteps, err := notifySinkSteps(n, connectors, msg, notes)
		if err != nil {
			return nil, err
		}
		digestSteps = append(digestSteps, stepsFor["digest"]...)
		if len(digestSteps) > 0 {
			out = append(out, config.TriggerSpec{
				Name:  "notify-digest",
				On:    "conductor.complete",
				Group: &config.GroupSpec{Key: `"digest"`, Window: n.Digest, MaxWait: n.Digest},
				Steps: digestSteps,
			})
			*notes = append(*notes, fmt.Sprintf("notify.digest %s → a grouped conductor.complete trigger (group window %s); the legacy report-style summary becomes a batched completion count", n.Digest.String(), n.Digest.String()))
		}
	}
	if n.Push {
		*notes = append(*notes, "notify.push had no delivery effect — dropped")
	}
	return out, nil
}

// applyNotifyPass rewrites a connectors-schema document that still carries a
// notify: block — the standalone face of the migration (the legacy Transform
// runs notifyToTriggers directly). It parses the block, generates the
// connectors + triggers, splices them into the node tree, and removes
// notify:.
func applyNotifyPass(masked []byte, notes *[]string) (out []byte, changed bool, err error) {
	var cfg config.Config
	if err := yaml.Unmarshal(masked, &cfg); err != nil {
		return nil, false, err
	}
	if !notifyConfigured(cfg.Notify) {
		return nil, false, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(masked, &doc); err != nil {
		return nil, false, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, nil
	}
	root := doc.Content[0]

	// Preseed the collision universe with the existing connector names; the
	// sentinel marks them so only freshly generated entries are spliced in.
	sentinel := map[string]any{"__existing__": true}
	connectors := map[string]map[string]any{}
	for name := range cfg.ConnectorsMap {
		connectors[name] = sentinel
	}
	triggers, err := notifyToTriggers(&cfg, connectors, notes)
	if err != nil {
		return nil, false, err
	}
	generated := map[string]map[string]any{}
	for name, conn := range connectors {
		if _, existing := conn["__existing__"]; !existing {
			generated[name] = conn
		}
	}

	// Remove notify: from the root.
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "notify" {
			root.Content = append(root.Content[:i], root.Content[i+2:]...)
			break
		}
	}
	// Append generated connectors into (or create) connectors:.
	if len(generated) > 0 {
		m := mapValue(root, "connectors")
		if m == nil {
			m = &yaml.Node{Kind: yaml.MappingNode}
			root.Content = append(root.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "connectors"}, m)
		}
		names := make([]string, 0, len(generated))
		for n := range generated {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			var val yaml.Node
			if err := val.Encode(generated[name]); err != nil {
				return nil, false, err
			}
			m.Content = append(m.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, &val)
		}
	}
	// Append the triggers into (or create) triggers:.
	if len(triggers) > 0 {
		var seq *yaml.Node
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == "triggers" && root.Content[i+1].Kind == yaml.SequenceNode {
				seq = root.Content[i+1]
				break
			}
		}
		if seq == nil {
			seq = &yaml.Node{Kind: yaml.SequenceNode}
			root.Content = append(root.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "triggers"}, seq)
		}
		for _, tr := range triggers {
			var val yaml.Node
			if err := val.Encode(tr); err != nil {
				return nil, false, err
			}
			seq.Content = append(seq.Content, &val)
		}
	}
	b, err := marshalDoc(&doc)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

