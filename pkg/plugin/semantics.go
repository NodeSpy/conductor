package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Semantics are how a plugin tells the engine what to DO with its events and
// verbs, in generic terms the engine implements once for every plugin
// (docs/design/plugin-contract.md §2). The engine never keys behavior on a
// plugin's event or verb NAMES; it keys on these declarations.
//
// Declarations are MUST-UNDERSTAND: a key the host does not implement refuses
// the plugin at load (CheckSemantics), because silently ignoring "dedupe on
// this cursor" or "this event closes its target" would silently change what
// the engine does. A plugin that can live without one lists its key in
// Optional, and an older host then ignores it.

// EventSemantics are the semantics of one event (Event.Semantics).
type EventSemantics struct {
	Target             *TargetSemantics       `json:"target,omitempty"`
	Revision           *RevisionSemantics     `json:"revision,omitempty"`
	Checkout           *CheckoutSemantics     `json:"checkout,omitempty"`
	ClosesTarget       *ClosesTargetSemantics `json:"closes_target,omitempty"`
	BoundToTarget      bool                   `json:"bound_to_target,omitempty"`
	Completion         *Completion            `json:"completion,omitempty"`
	Cursor             *CursorSemantics       `json:"cursor,omitempty"`
	Attempts           *AttemptsSemantics     `json:"attempts,omitempty"`
	Priority           string                 `json:"priority,omitempty"` // interactive | normal
	Feedback           bool                   `json:"feedback,omitempty"`
	VerificationFailed bool                   `json:"verification_failed,omitempty"`
	Remediate          *RemediateSemantics    `json:"remediate,omitempty"`
	Author             *AuthorSemantics       `json:"author,omitempty"`
	Labels             string                 `json:"labels,omitempty"`
	Private            []string               `json:"private,omitempty"`
	Secret             []string               `json:"secret,omitempty"`
	ConversationReply  *ConversationReply     `json:"conversation_reply,omitempty"`
	// Optional lists semantic keys an older host may ignore.
	Optional []string `json:"optional,omitempty"`
}

// TargetSemantics: what an event is about. Key is a template over the
// event's facts; Assigned (a bool, or the name of a bool fact) says the
// platform chose the target rather than the sender; Scope maps scope
// dimensions to the facts that carry their values.
type TargetSemantics struct {
	Key      string          `json:"key,omitempty"`
	URL      string          `json:"url,omitempty"`
	Label    string          `json:"label,omitempty"`
	Assigned json.RawMessage `json:"assigned,omitempty"`
	Scope    []ScopeFact     `json:"scope,omitempty"`
}

// ScopeFact binds one scope dimension to the fact carrying its value.
type ScopeFact struct {
	Dimension string `json:"dimension"`
	Fact      string `json:"fact"`
}

// RevisionSemantics: the fact naming the target's current revision, and the
// branch / base it lives on.
type RevisionSemantics struct {
	Fact   string `json:"fact"`
	Branch string `json:"branch,omitempty"`
	Base   string `json:"base,omitempty"`
}

// CheckoutSemantics: how an agent workspace gets the target's code. Absent
// means no checkout (a synthetic target). RuntimeHints are passed to the
// agent runtime unread.
type CheckoutSemantics struct {
	Remote       string            `json:"remote"`
	FetchRef     string            `json:"fetch_ref,omitempty"`
	PushBranch   string            `json:"push_branch,omitempty"`
	RuntimeHints map[string]string `json:"runtime_hints,omitempty"`
}

// ClosesTargetSemantics: this event is terminal for its target.
type ClosesTargetSemantics struct {
	Outcome *OutcomeMap  `json:"outcome,omitempty"`
	Reverts *RevertsFact `json:"reverts,omitempty"`
}

// OutcomeMap maps a bool fact to an outcome (accepted | rejected).
type OutcomeMap struct {
	Fact  string `json:"fact"`
	True  string `json:"true"`
	False string `json:"false"`
}

// RevertsFact names the fact listing the sibling targets this one reverts,
// and the fact saying the claim is corroborated.
type RevertsFact struct {
	Fact         string `json:"fact"`
	Corroborated string `json:"corroborated,omitempty"`
}

// Completion is `edge` (the default: dedupe, record done, cap attempts) or
// `{level: {rearm_on}}` (external state a poll re-derives: never recorded
// done, skipped while an agent is live, optionally re-armed when the
// target's revision changes). On the wire: "edge", "level", or
// {"level": {...}}.
type Completion struct {
	Level *LevelCompletion
}

// LevelCompletion is the level-triggered form's options.
type LevelCompletion struct {
	RearmOn string `json:"rearm_on,omitempty"` // "" | revision
}

// MarshalJSON writes "edge" or {"level": {...}}.
func (c Completion) MarshalJSON() ([]byte, error) {
	if c.Level == nil {
		return json.Marshal("edge")
	}
	return json.Marshal(map[string]any{"level": c.Level})
}

// UnmarshalJSON reads "edge", "level", {"edge": {}} or {"level": {...}}.
func (c *Completion) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		switch s {
		case "edge":
			c.Level = nil
		case "level":
			c.Level = &LevelCompletion{}
		default:
			return fmt.Errorf("completion must be edge or level, got %q", s)
		}
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("completion: %w", err)
	}
	if len(m) != 1 {
		return fmt.Errorf("completion must name exactly one of edge, level")
	}
	if raw, ok := m["level"]; ok {
		var l LevelCompletion
		if err := strictDecode(raw, &l); err != nil {
			return fmt.Errorf("completion.level: %w", err)
		}
		if l.RearmOn != "" && l.RearmOn != "revision" {
			return fmt.Errorf("completion.level.rearm_on must be revision, got %q", l.RearmOn)
		}
		c.Level = &l
		return nil
	}
	if _, ok := m["edge"]; ok {
		c.Level = nil
		return nil
	}
	return fmt.Errorf("completion must name edge or level")
}

// CursorSemantics: a monotonic high-water mark on a numeric fact, per
// target, event variant and (optionally templated) stream.
type CursorSemantics struct {
	ID     string `json:"id"`
	Stream string `json:"stream,omitempty"`
}

// AttemptsSemantics: per_revision (default) or none (each event is a
// distinct item, outside the attempt cap).
type AttemptsSemantics struct {
	Cap string `json:"cap"`
}

// RemediateSemantics: a pre-dispatch remedy the operator turns on per
// trigger with options.<Option>.
type RemediateSemantics struct {
	Option string         `json:"option"`
	Run    string         `json:"run"`
	Status RemediateCheck `json:"status"`
	Action RemediateVerb  `json:"action"`
	Budget int            `json:"budget,omitempty"`
}

// RemediateCheck is the status read: a verb, and the expr that says the
// run has finished.
type RemediateCheck struct {
	Verb     string            `json:"verb"`
	Args     map[string]string `json:"args,omitempty"` // option → template over the event's facts
	DoneWhen string            `json:"done_when"`
}

// RemediateVerb is the remedy itself.
type RemediateVerb struct {
	Verb string            `json:"verb"`
	Args map[string]string `json:"args,omitempty"`
}

// AuthorSemantics: who caused the event, and whether they are automated.
type AuthorSemantics struct {
	Login     string `json:"login"`
	Automated string `json:"automated,omitempty"`
}

// ConversationReply: a reply in a conversation an opens_conversation verb
// started. ID is a template over the event's facts.
type ConversationReply struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	Text   string `json:"text"`
}

// VerbSemantics are the semantics of one verb (Verb.Semantics).
type VerbSemantics struct {
	ReadsRevision     *ReadsRevision     `json:"reads_revision,omitempty"`
	MintsCredential   *MintsCredential   `json:"mints_credential,omitempty"`
	OpensConversation *OpensConversation `json:"opens_conversation,omitempty"`
	ConversationPost  bool               `json:"conversation_post,omitempty"`
	HostOnly          bool               `json:"host_only,omitempty"`
	TargetArgs        map[string]string  `json:"target_args,omitempty"`
	Exposes           *Exposes           `json:"exposes,omitempty"`
	Optional          []string           `json:"optional,omitempty"`
}

// ReadsRevision: the verb that reads a target's current revision and state.
// States maps each generic state (open, closed, accepted) to the verb's own
// spellings of it.
type ReadsRevision struct {
	Args     map[string]string   `json:"args,omitempty"`
	Revision string              `json:"revision"`
	State    string              `json:"state,omitempty"`
	States   map[string][]string `json:"states,omitempty"`
	// Reasons is what a run stopped because its target went away reports
	// as its reason, per generic state ("closed", "accepted"); the engine
	// falls back to a neutral phrase.
	Reasons map[string]string `json:"reasons,omitempty"`
}

// MintsCredential: this verb mints the named connection credential.
type MintsCredential struct {
	Credential string `json:"credential"`
}

// OpensConversation: posting with this verb opens a conversation keyed by
// the output named ID; replies arrive as conversation_reply events.
type OpensConversation struct {
	ID        string `json:"id"`
	Approvers string `json:"approvers,omitempty"`
}

// Exposes: this verb makes a local address reachable from outside (a tunnel
// or a relay). Local is the option the host fills with host:port; URL and
// Lease are outputs; Release is the verb that ends the exposure.
type Exposes struct {
	Local   string `json:"local"`
	URL     string `json:"url"`
	Lease   string `json:"lease,omitempty"`
	Release string `json:"release,omitempty"`
}

// ConnSemantics are connection-level semantics (Decl.Semantics).
type ConnSemantics struct {
	Credentials []Credential        `json:"credentials,omitempty"`
	Scope       *ConnScope          `json:"scope,omitempty"`
	Poll        *PollSemantics      `json:"poll,omitempty"`
	Translate   *TranslateSemantics `json:"translate,omitempty"`
	Listeners   []Listener          `json:"listeners,omitempty"`
	Preflight   *PreflightSemantics `json:"preflight,omitempty"`
	Optional    []string            `json:"optional,omitempty"`
}

// Credential is one credential agents dispatched on this connection's
// events receive.
type Credential struct {
	Name string         `json:"name"`
	Role string         `json:"role"` // read | write
	Mint CredentialMint `json:"mint"`
	// Value is the mint verb's output carrying the credential (default
	// "token").
	Value     string            `json:"value,omitempty"`
	Env       []string          `json:"env,omitempty"`
	Template  string            `json:"template,omitempty"`
	Expires   string            `json:"expires,omitempty"`
	Refresh   string            `json:"refresh,omitempty"` // resume | expiry
	GitAuthor *GitAuthorOutputs `json:"git_author,omitempty"`
	Guidance  string            `json:"guidance,omitempty"`
}

// CredentialMint is the verb (and its templated args) that mints a
// credential.
type CredentialMint struct {
	Verb string            `json:"verb"`
	Args map[string]string `json:"args,omitempty"`
}

// GitAuthorOutputs names the mint verb's outputs carrying the commit author.
type GitAuthorOutputs struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// ConnScope: the connection's operator-chosen scope dimension and the
// connection option that sets it.
type ConnScope struct {
	Dimension string `json:"dimension"`
	Option    string `json:"option"`
	Consent   bool   `json:"consent,omitempty"`
}

// PollSemantics: the engine provides a `<conn>.<VerbName>` verb that polls
// this instance now (default name "poll").
type PollSemantics struct {
	VerbName string `json:"verb_name,omitempty"`
}

// TranslateSemantics: where `conductor once` finds a delivery in a CI
// runner's environment.
type TranslateSemantics struct {
	Env map[string]string `json:"env,omitempty"` // event_path, event_name → env var
}

// Listener: an inbound listener the plugin runs, which an exposure
// connector can make reachable (config field paths, dot-separated).
type Listener struct {
	Listen string `json:"listen"`
	Expose string `json:"expose,omitempty"`
	URLTo  string `json:"url_to,omitempty"`
}

// PreflightSemantics: commands checked at boot.
type PreflightSemantics struct {
	Commands []string `json:"commands,omitempty"`
}

// --- must-understand -------------------------------------------------------

// semanticKeys are the keys this SDK (and the host built with it)
// implements, per level. HostInfo.Semantics advertises them.
var semanticKeys = map[string][]string{
	"event": {"target", "revision", "checkout", "closes_target", "bound_to_target", "completion", "cursor",
		"attempts", "priority", "feedback", "verification_failed", "remediate", "author", "labels",
		"private", "secret", "conversation_reply"},
	"verb": {"reads_revision", "mints_credential", "opens_conversation", "conversation_post", "host_only",
		"target_args", "exposes"},
	"connection": {"credentials", "scope", "poll", "translate", "listeners", "preflight"},
}

// KnownSemantics lists every semantic this SDK implements, as
// "<level>.<key>" (event.cursor, verb.exposes, connection.credentials).
func KnownSemantics() []string {
	var out []string
	for level, keys := range semanticKeys {
		for _, k := range keys {
			out = append(out, level+"."+k)
		}
	}
	sort.Strings(out)
	return out
}

func known(level, key string) bool {
	for _, k := range semanticKeys[level] {
		if k == key {
			return true
		}
	}
	return false
}

// CheckSemantics enforces must-understand on a raw describe result: every
// semantics key at every level must be one this host implements, unless the
// block lists it in `optional`, and every known key must decode strictly (no
// unknown nested fields). It returns every problem found, each naming its
// path.
func CheckSemantics(rawDecl []byte) []string {
	var d struct {
		Semantics json.RawMessage `json:"semantics"`
		Events    []struct {
			Name      string          `json:"name"`
			Semantics json.RawMessage `json:"semantics"`
		} `json:"events"`
		Verbs []struct {
			Name      string          `json:"name"`
			Semantics json.RawMessage `json:"semantics"`
		} `json:"verbs"`
	}
	if err := json.Unmarshal(rawDecl, &d); err != nil {
		return []string{"describe: " + err.Error()}
	}
	var problems []string
	problems = append(problems, checkBlock("semantics", "connection", d.Semantics, &ConnSemantics{})...)
	for _, e := range d.Events {
		problems = append(problems, checkBlock("events["+e.Name+"].semantics", "event", e.Semantics, &EventSemantics{})...)
	}
	for _, v := range d.Verbs {
		problems = append(problems, checkBlock("verbs["+v.Name+"].semantics", "verb", v.Semantics, &VerbSemantics{})...)
	}
	return problems
}

func checkBlock(path, level string, raw json.RawMessage, into any) []string {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return []string{path + ": " + err.Error()}
	}
	optional := map[string]bool{}
	if o, ok := m["optional"]; ok {
		var keys []string
		if err := json.Unmarshal(o, &keys); err != nil {
			return []string{path + ".optional: must be a list of semantic keys"}
		}
		for _, k := range keys {
			optional[k] = true
		}
	}
	var problems []string
	kept := map[string]json.RawMessage{}
	for k, v := range m {
		if k == "optional" {
			continue
		}
		if !known(level, k) {
			if !optional[k] {
				problems = append(problems, fmt.Sprintf("%s.%s: this conductor does not implement this semantic (a newer conductor is needed, or the plugin must list it in optional)", path, k))
			}
			continue
		}
		kept[k] = v
	}
	// Known keys decode strictly: an unknown NESTED field is the same
	// must-understand violation one level down.
	b, _ := json.Marshal(kept)
	if err := strictDecode(b, into); err != nil {
		problems = append(problems, path+": "+err.Error())
	}
	sort.Strings(problems)
	return problems
}

func strictDecode(b []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}

// StripUnknownOptional removes, from a Decl about to be sent to a host that
// advertised hostKnows ("<level>.<key>"), every semantic the Decl marked
// optional and the host does not implement. Serve calls it on describe, so a
// plugin built against a newer SDK still loads on an older host.
func StripUnknownOptional(d *Decl, hostKnows []string) {
	if len(hostKnows) == 0 {
		return
	}
	set := map[string]bool{}
	for _, k := range hostKnows {
		set[k] = true
	}
	drop := func(level string, optional []string) map[string]bool {
		out := map[string]bool{}
		for _, k := range optional {
			if !set[level+"."+k] {
				out[k] = true
			}
		}
		return out
	}
	if d.Semantics != nil {
		d.Semantics = stripStruct(d.Semantics, drop("connection", d.Semantics.Optional))
	}
	for i := range d.Events {
		if s := d.Events[i].Semantics; s != nil {
			d.Events[i].Semantics = stripStruct(s, drop("event", s.Optional))
		}
	}
	for i := range d.Verbs {
		if s := d.Verbs[i].Semantics; s != nil {
			d.Verbs[i].Semantics = stripStruct(s, drop("verb", s.Optional))
		}
	}
}

// stripStruct round-trips v through a map, deleting keys in drop.
func stripStruct[T any](v *T, drop map[string]bool) *T {
	if len(drop) == 0 {
		return v
	}
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return v
	}
	for k := range drop {
		delete(m, k)
	}
	b, _ = json.Marshal(m)
	var out T
	if json.Unmarshal(b, &out) != nil {
		return v
	}
	return &out
}

// --- consistency -------------------------------------------------------------

// ValidateSemantics checks a Decl's semantics for internal consistency: every
// verb a semantic names exists and carries the semantic that makes it fit,
// required fields are present, and enumerations hold. The host refuses a
// plugin whose declarations do not hang together, so the engine never acts
// on half a declaration.
func ValidateSemantics(d Decl) []string {
	verbs := map[string]*VerbSemantics{}
	for _, v := range d.Verbs {
		verbs[v.Name] = v.Semantics
		if v.Semantics == nil {
			verbs[v.Name] = &VerbSemantics{}
		}
	}
	var p []string
	need := func(path, verb string) *VerbSemantics {
		s, ok := verbs[verb]
		if !ok {
			p = append(p, fmt.Sprintf("%s: names verb %q, which the plugin does not declare", path, verb))
			return nil
		}
		return s
	}
	for _, e := range d.Events {
		s := e.Semantics
		if s == nil {
			continue
		}
		path := "events[" + e.Name + "].semantics"
		if s.Revision != nil && s.Revision.Fact == "" {
			p = append(p, path+".revision.fact: required")
		}
		if s.Checkout != nil && s.Checkout.Remote == "" {
			p = append(p, path+".checkout.remote: required")
		}
		if s.Cursor != nil && s.Cursor.ID == "" {
			p = append(p, path+".cursor.id: required")
		}
		if s.Attempts != nil && s.Attempts.Cap != "per_revision" && s.Attempts.Cap != "none" {
			p = append(p, path+".attempts.cap: must be per_revision or none")
		}
		if s.Priority != "" && s.Priority != "interactive" && s.Priority != "normal" {
			p = append(p, path+".priority: must be interactive or normal")
		}
		if s.Author != nil && s.Author.Login == "" {
			p = append(p, path+".author.login: required")
		}
		if c := s.ClosesTarget; c != nil && c.Outcome != nil {
			for _, v := range []string{c.Outcome.True, c.Outcome.False} {
				if v != "accepted" && v != "rejected" {
					p = append(p, path+".closes_target.outcome: values must be accepted or rejected")
					break
				}
			}
		}
		if r := s.ConversationReply; r != nil && (r.ID == "" || r.Author == "" || r.Text == "") {
			p = append(p, path+".conversation_reply: id, author and text are required")
		}
		if r := s.Remediate; r != nil {
			if r.Option == "" || r.Run == "" || r.Status.DoneWhen == "" {
				p = append(p, path+".remediate: option, run and status.done_when are required")
			}
			need(path+".remediate.status.verb", r.Status.Verb)
			need(path+".remediate.action.verb", r.Action.Verb)
		}
	}
	for _, v := range d.Verbs {
		s := v.Semantics
		if s == nil {
			continue
		}
		path := "verbs[" + v.Name + "].semantics"
		if s.ReadsRevision != nil && s.ReadsRevision.Revision == "" {
			p = append(p, path+".reads_revision.revision: required")
		}
		if x := s.Exposes; x != nil {
			if x.Local == "" || x.URL == "" {
				p = append(p, path+".exposes: local and url are required")
			}
			if x.Release != "" {
				need(path+".exposes.release", x.Release)
			}
		}
		if s.MintsCredential != nil && !s.HostOnly {
			p = append(p, path+": a mints_credential verb must be host_only, so a credential never lands in a step's outputs")
		}
		if s.Exposes != nil && !s.HostOnly {
			p = append(p, path+": an exposes verb must be host_only, so a flow step or agent cannot tunnel an arbitrary local address")
		}
	}
	if c := d.Semantics; c != nil {
		names := map[string]bool{}
		for i, cr := range c.Credentials {
			path := fmt.Sprintf("semantics.credentials[%d]", i)
			if cr.Name == "" || names[cr.Name] {
				p = append(p, path+".name: required and unique")
			}
			names[cr.Name] = true
			if cr.Role != "read" && cr.Role != "write" {
				p = append(p, path+".role: must be read or write")
			}
			if cr.Refresh != "" && cr.Refresh != "resume" && cr.Refresh != "expiry" {
				p = append(p, path+".refresh: must be resume or expiry")
			}
			if s := need(path+".mint.verb", cr.Mint.Verb); s != nil &&
				(s.MintsCredential == nil || s.MintsCredential.Credential != cr.Name) {
				p = append(p, fmt.Sprintf("%s.mint.verb: verb %q must declare mints_credential: {credential: %s}", path, cr.Mint.Verb, cr.Name))
			}
		}
		if s := c.Scope; s != nil && (s.Dimension == "" || s.Option == "") {
			p = append(p, "semantics.scope: dimension and option are required")
		}
		for i, l := range c.Listeners {
			if l.Listen == "" {
				p = append(p, fmt.Sprintf("semantics.listeners[%d].listen: required", i))
			}
		}
	}
	sort.Strings(p)
	return p
}

// FactName strips a template's braces when it names a single fact
// ("{{.repo}}" → "repo"); other strings come back unchanged.
func FactName(s string) string {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "{{.") && strings.HasSuffix(t, "}}") && !strings.ContainsAny(t[3:len(t)-2], " |{}") {
		return t[3 : len(t)-2]
	}
	return s
}
