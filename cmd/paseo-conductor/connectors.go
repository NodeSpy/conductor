package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/connector"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/flow"
	"github.com/NodeSpy/paseo-conductor/internal/handoff"
	"github.com/NodeSpy/paseo-conductor/internal/inbound"
	"github.com/NodeSpy/paseo-conductor/internal/integrations/slack"
	"github.com/NodeSpy/paseo-conductor/internal/notify"
	"github.com/NodeSpy/paseo-conductor/internal/secrets"
)

// flowStack is the connectors-model runtime: the secret resolver, the built
// connector registry, the flow runner, and the lowered source integrations.
type flowStack struct {
	Secrets      *secrets.Resolver
	Registry     *connector.Registry
	Runner       *flow.Runner
	Integrations []core.Integration
	SecretErrs   []string
	// ConnectorErrs names each connector disabled by a credential/build
	// failure (not by an authored enabled: false) — #36 requires disable AND
	// notify, so main routes these through the notifier at boot.
	ConnectorErrs []string
}

// buildFlowStack builds the connectors-model pieces from a loaded config.
// Returns (nil, nil) when the config has no connectors: block. flowStore/
// flowNotif may be nil for validate-only callers.
func buildFlowStack(cfg *config.Config, flowStore flow.Store, flowNotif flow.Notifier, dryRun bool) (*flowStack, error) {
	if !cfg.HasConnectors() {
		return nil, nil
	}
	sec := secrets.New()
	vals := map[string]string{}
	var secretErrs []string
	names := make([]string, 0, len(cfg.SecretRefs))
	for n := range cfg.SecretRefs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		v, err := sec.Resolve(context.Background(), cfg.SecretRefs[name])
		if err != nil {
			// A named secret that won't resolve must not crash-loop the box:
			// leave it empty (steps reading it get ""), record the failure so
			// boot logs and `secrets check` name it.
			secretErrs = append(secretErrs, fmt.Sprintf("secrets.%s: %v", name, err))
			continue
		}
		vals[name] = v
	}

	deps := connector.Deps{Secrets: sec, Log: logf, Config: cfg}
	reg, err := connector.Build(cfg, deps)
	if err != nil {
		return nil, err
	}
	if err := flow.Validate(cfg, reg); err != nil {
		return nil, err
	}

	// Lower each connector's triggers into its source integration.
	byConn := map[string][]connector.CompiledTrigger{}
	for i, spec := range cfg.Triggers {
		byConn[spec.Connector()] = append(byConn[spec.Connector()], connector.CompiledTrigger{Index: i, Spec: spec})
	}
	var igs []core.Integration
	var connErrs []string
	for _, name := range reg.Names() {
		in, _ := reg.Get(name)
		trigs := byConn[name]
		if !in.Enabled {
			if len(trigs) > 0 {
				logf("connector %q disabled — %d trigger(s) inert", name, len(trigs))
			}
			continue
		}
		if in.DisabledReason != "" {
			logf("connector %q disabled (%s)%s", name, in.DisabledReason, inertNote(len(trigs)))
			connErrs = append(connErrs, fmt.Sprintf("connector %q disabled: %s%s", name, in.DisabledReason, inertNote(len(trigs))))
			continue
		}
		src, err := in.Impl.Source(trigs)
		if err != nil {
			return nil, fmt.Errorf("connector %q: %w", name, err)
		}
		if src == nil {
			continue
		}
		if err := src.Validate(); err != nil {
			return nil, err
		}
		igs = append(igs, src)
	}

	runner := flow.New(flow.Runner{
		Cfg: cfg, Conns: reg, Secrets: sec, SecretVals: vals,
		Store: flowStore, Notif: flowNotif, Log: logf, DryRun: dryRun,
	})
	return &flowStack{
		Secrets: sec, Registry: reg, Runner: runner,
		Integrations: igs, SecretErrs: secretErrs, ConnectorErrs: connErrs,
	}, nil
}

// stackEmitter is the notifier surface notifyStackFailures needs (satisfied
// by *notify.Notifier; a test fake captures the emissions).
type stackEmitter interface {
	Emit(ctx context.Context, event string, t core.Trigger, msg string)
}

// notifyStackFailures escalates the failures buildFlowStack collected: named
// secrets that would not resolve and connectors disabled by credential/build
// errors. Both must be visible, not just logged — a bad connector never
// crash-loops the box, so a notification is the operator's only signal.
func notifyStackFailures(stack *flowStack, n stackEmitter) {
	if stack == nil {
		return
	}
	for _, e := range stack.SecretErrs {
		logf("secrets: %s", e)
		n.Emit(context.Background(), notify.EventEscalate, core.Trigger{Source: "secrets", Kind: "secret_unresolved"}, e)
	}
	for _, e := range stack.ConnectorErrs {
		n.Emit(context.Background(), notify.EventEscalate, core.Trigger{Source: "connectors", Kind: "connector_disabled"}, e)
	}
}

func inertNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" — %d trigger(s) inert", n)
}

// webConnector / discordConnector / slackConnector are the duck-typed wiring
// surfaces connector Impls expose for main (see internal/connector).
type webConnector interface {
	Channel() *handoff.WebChannel
	Listen() string
}

type discordConnector interface {
	BotToken() string
	Inbox() *handoff.Inbox
}

type slackInboxer interface {
	Inbox() *handoff.Inbox
}

// wireConnectorSurfaces mounts web connectors' draft pages on the inbound
// listener, starts discord connectors' gateways, and fans Socket Mode replies
// into every slack inbox (legacy handoffs + slack connectors).
func wireConnectorSurfaces(ctx context.Context, stack *flowStack, handoffs *handoff.Registry, cfg *config.Config) {
	if stack == nil {
		wireSlackHandoffInbox(cfg, handoffs)
		return
	}
	var slackInboxes []*handoff.Inbox
	if legacy := handoffs.SlackInbox(); legacy != nil {
		slackInboxes = append(slackInboxes, legacy)
	}
	seenGateway := map[string]bool{}
	for _, name := range stack.Registry.Names() {
		in, _ := stack.Registry.Get(name)
		if in.Impl == nil || !in.Enabled || in.DisabledReason != "" {
			continue
		}
		if wc, ok := in.Impl.(webConnector); ok {
			if ch := wc.Channel(); ch != nil {
				inbound.Register(ctx, wc.Listen(), "/handoff", ch, logf)
				logf("connector %s: web ask pages on %s/handoff", name, wc.Listen())
			}
		}
		if dc, ok := in.Impl.(discordConnector); ok {
			if tok := dc.BotToken(); tok != "" && !seenGateway[tok] {
				seenGateway[tok] = true
				go handoff.RunDiscordGateway(ctx, tok, dc.Inbox(), logf)
				logf("connector %s: discord gateway starting", name)
			}
		}
		if si, ok := in.Impl.(slackInboxer); ok {
			if ib := si.Inbox(); ib != nil {
				slackInboxes = append(slackInboxes, ib)
			}
		}
	}
	if len(slackInboxes) > 0 {
		inboxes := slackInboxes
		slack.SetReplyHook(func(channel, threadTS, _, text string) bool {
			for _, ib := range inboxes {
				if ib.Deliver(channel, threadTS, text) {
					return true
				}
			}
			return false
		})
	}
}

// cmdConnectors implements `conductor connectors ls`: every configured
// connector with its type, state, and event/verb surface.
func cmdConnectors(args []string) error {
	cfg, _, err := loadConfig(args)
	if err != nil {
		return err
	}
	rest := positional(args)
	if len(rest) > 0 && rest[0] != "ls" {
		return fmt.Errorf("usage: conductor connectors ls")
	}
	if !cfg.HasConnectors() {
		fmt.Println("no connectors: block configured (legacy integrations: config)")
		return nil
	}
	stack, err := buildFlowStack(cfg, nil, nil, true)
	if err != nil {
		return err
	}
	trigCount := map[string]int{}
	for _, t := range cfg.Triggers {
		trigCount[t.Connector()]++
	}
	for _, name := range stack.Registry.Names() {
		in, _ := stack.Registry.Get(name)
		state := "enabled"
		if !in.Enabled {
			state = "disabled (enabled: false)"
		} else if in.DisabledReason != "" {
			state = "disabled: " + in.DisabledReason
		}
		fmt.Printf("%-14s %-10s %s\n", name, in.Decl.Type, state)
		events := in.Decl.EventNames()
		if in.Impl != nil {
			if dyn := in.Impl.DeclaredEvents(); len(dyn) > 0 {
				events = dyn
			}
		}
		if len(events) > 0 {
			fmt.Printf("  events: %s\n", strings.Join(events, ", "))
		}
		if verbs := in.Decl.VerbNames(); len(verbs) > 0 {
			fmt.Printf("  verbs:  %s\n", strings.Join(verbs, ", "))
		}
		fmt.Printf("  triggers: %d\n", trigCount[name])
	}
	return nil
}

// cmdSchema implements `conductor schema <connector>`: the full published
// event/filter/context/option/output schemas for one connector.
func cmdSchema(args []string) error {
	cfg, _, err := loadConfig(args)
	if err != nil {
		return err
	}
	rest := positional(args)
	if len(rest) != 1 {
		return fmt.Errorf("usage: conductor schema <connector>")
	}
	name := rest[0]
	ref, ok := cfg.ConnectorsMap[name]
	if !ok {
		// Allow a bare type name too (schema for an unconfigured type).
		if decl, tok := connector.TypeDeclFor(name); tok {
			printTypeDecl(decl, nil)
			return nil
		}
		return fmt.Errorf("no connector %q configured (and no such type); types: %s", name, strings.Join(connector.Types(), ", "))
	}
	decl, ok := connector.TypeDeclFor(ref.Type)
	if !ok {
		return fmt.Errorf("connector %q has unknown type %q", name, ref.Type)
	}
	var dyn []string
	if stack, err := buildFlowStack(cfg, nil, nil, true); err == nil {
		if in, ok := stack.Registry.Get(name); ok && in.Impl != nil {
			dyn = in.Impl.DeclaredEvents()
		}
	}
	fmt.Printf("connector %s (type %s)\n", name, ref.Type)
	printTypeDecl(decl, dyn)
	return nil
}

func printTypeDecl(d *connector.TypeDecl, dynamicEvents []string) {
	if d.Desc != "" {
		fmt.Println(d.Desc)
	}
	if len(d.Connection) > 0 {
		fmt.Println("\nconnection:")
		printSchema(d.Connection, "  ")
	}
	for _, ev := range d.Events {
		name := ev.Name
		if ev.Dynamic {
			name = "<declared in connection>"
			if len(dynamicEvents) > 0 {
				name = strings.Join(dynamicEvents, ", ")
			}
		}
		fmt.Printf("\nevent %s", name)
		if ev.Desc != "" {
			fmt.Printf(" — %s", ev.Desc)
		}
		fmt.Println()
		if len(ev.Filters) > 0 {
			fmt.Println("  filters:")
			printSchema(ev.Filters, "    ")
		}
		if len(ev.Options) > 0 {
			fmt.Println("  options:")
			printSchema(ev.Options, "    ")
		}
		if len(ev.Context) > 0 {
			fmt.Println("  context:")
			printSchema(ev.Context, "    ")
		}
	}
	for _, v := range d.Verbs {
		fmt.Printf("\nverb %s", v.Name)
		if v.Ask {
			fmt.Printf(" (request-response)")
		}
		if v.Desc != "" {
			fmt.Printf(" — %s", v.Desc)
		}
		fmt.Println()
		if len(v.Options) > 0 {
			fmt.Println("  options:")
			printSchema(v.Options, "    ")
		}
		if len(v.Outputs) > 0 {
			fmt.Println("  outputs:")
			printSchema(v.Outputs, "    ")
		}
	}
}

func printSchema(s connector.Schema, indent string) {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f := s[k]
		line := indent + k
		typ := string(f.Type)
		if typ == "" {
			typ = "any"
		}
		if len(f.Enum) > 0 {
			typ = strings.Join(f.Enum, "|")
		}
		line += " (" + typ
		if f.Required {
			line += ", required"
		}
		line += ")"
		if f.Desc != "" {
			line += " — " + f.Desc
		}
		fmt.Println(line)
	}
}

// cmdSecrets implements `conductor secrets check`: resolve every secret
// reference in the config and report each one's state without printing values.
func cmdSecrets(args []string) error {
	cfg, _, err := loadConfig(args)
	if err != nil {
		return err
	}
	rest := positional(args)
	if len(rest) != 1 || rest[0] != "check" {
		return fmt.Errorf("usage: conductor secrets check")
	}
	sec := secrets.New()
	bad := 0
	names := make([]string, 0, len(cfg.SecretRefs))
	for n := range cfg.SecretRefs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		ref := cfg.SecretRefs[n]
		if _, err := sec.Resolve(context.Background(), ref); err != nil {
			fmt.Printf("FAIL secrets.%s (%s): %v\n", n, ref, err)
			bad++
		} else {
			fmt.Printf("ok   secrets.%s (%s)\n", n, redactRef(ref))
		}
	}
	// Connector construction resolves each connection's credential refs; a
	// failure surfaces as a disabled connector.
	if cfg.HasConnectors() {
		stack, err := buildFlowStack(cfg, nil, nil, true)
		if err != nil {
			return err
		}
		for _, name := range stack.Registry.Names() {
			in, _ := stack.Registry.Get(name)
			switch {
			case !in.Enabled:
				fmt.Printf("--   connector %s (disabled in config)\n", name)
			case in.DisabledReason != "":
				fmt.Printf("FAIL connector %s: %s\n", name, in.DisabledReason)
				bad++
			default:
				fmt.Printf("ok   connector %s\n", name)
			}
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d secret reference(s) failed to resolve", bad)
	}
	fmt.Println("all secret references resolve")
	return nil
}

// redactRef hides the tail of literal-looking refs in output.
func redactRef(ref string) string {
	if secrets.IsRef(ref) {
		return ref // scheme refs are locations, not values
	}
	return "(literal)"
}

// positional returns args minus --config/--flag pairs.
func positional(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--config" && i+1 < len(args) {
			i++
			continue
		}
		if strings.HasPrefix(a, "--") {
			continue
		}
		out = append(out, a)
	}
	return out
}
