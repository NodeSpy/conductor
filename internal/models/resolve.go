package models

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// The resolution ladder (docs/design/runtimes-models-packs.md §2.3).
//
// Per fleet reference:
//
//  1. a CONSUMER OVERRIDE (packs.<pack>.models.<fleet>: <model>) → use it;
//  2. else INTERSECT the fleet's any: (wildcards expanded) with the union of
//     the configured runtimes' rosters, ranked by prefer: → run the winner on
//     the runtime that offers it;
//  3. else if required: false → BARE LAUNCH;
//  4. else (required: true, nothing matched) → a hard error naming the fleet
//     and acceptable-vs-available.
//
// A step that names no model at all takes the runtime's models.default:, then
// its first available prefer: entry, then bare launch (§3.2).
//
// BARE LAUNCH is a first-class outcome, not a failure: conductor dispatches
// with no --model and the runtime uses its own built-in default. It is
// distinct from `model: "*"`, which resolves to a CONCRETE model via prefer:.

// Decision is one resolved model choice.
type Decision struct {
	// Model is the id to pass as --model. Empty means BARE LAUNCH.
	Model string
	// Runtime is the runtimes: entry that offers Model. Empty leaves the
	// caller's own runtime selection alone (bare launch, or a pass-through
	// pin that no roster confirmed).
	Runtime string
	// Provider is the catalog provider Model belongs to (paseo run --provider),
	// taken from the roster entry that confirmed the pick. Empty for a genuine
	// BARE launch or a pass-through pin no roster verified — the runtime (or
	// paseo itself) then falls back to its own default provider resolution.
	Provider string
	// Bare reports the bare-launch outcome explicitly, so a caller never has
	// to infer it from an empty Model.
	Bare bool
	// Reason explains the rung of the ladder this came off, for run logs and
	// `conductor validate`.
	Reason string
	// Notice is a non-fatal thing an operator should see — an exact pin no
	// runtime offers, a fleet that fell through to bare launch. Empty when
	// there is nothing to say.
	Notice string
}

// Resolver answers "which model, on which runtime" for a step. Rosters are
// discovered once per runtime per Resolver and memoized, so a fan-out of
// dispatches costs one discovery pass.
type Resolver struct {
	cfg *config.Config
	cat *Catalog

	// Overrides are consumer fleet overrides — rung 1 of the ladder. Keyed by
	// fleet name (a pack instance's `models:` overlay lowers into this).
	Overrides map[string]string

	// Excluded skips a (runtime, model) candidate during ranking — wired to the
	// UnsupportedCache so a model a provider refused at run time (e.g. "client
	// too old for this model") falls through to the next fleet candidate on
	// re-resolution instead of being picked again. nil = nothing excluded.
	Excluded func(runtime, model string) bool

	// NativeProtocols reports the decision protocols a runtime answers
	// natively (a decision runtime's declared Protocols). nil, or an empty
	// answer, means the runtime speaks none — every agent runtime.
	NativeProtocols func(runtime string) []string
	// DecisionOnly reports a runtime that serves ONLY decide: steps. Agent
	// resolution never considers one, so an agent step can never land on a
	// decision runtime — not even when its fleet lists that runtime's
	// models. nil means no runtime is decision-only.
	DecisionOnly func(runtime string) bool

	// now is the clock, injectable so the failure TTL is testable without
	// sleeping. nil means time.Now.
	now func() time.Time

	mu      sync.Mutex
	rosters map[string]Roster
	failed  map[string]failure
	// inflight dedups concurrent cold discovery per runtime: the second
	// caller waits on the first's channel instead of issuing its own
	// List. See roster.
	inflight map[string]chan struct{}
}

// FailureTTL is how long a discovery failure is remembered before the runtime
// is probed again.
//
// It exists because the negative cache used to be PERMANENT. One failed probe
// — a daemon mid-restart, a network blip, a provider rate limit — and that
// runtime enumerated nothing for the entire life of the process: every fleet
// matched nothing, every dispatch degraded to a bare launch, and the only
// cure was restarting conductor. A positive result is still cached forever
// (the roster is stable within a process); only the failure expires.
const FailureTTL = 10 * time.Minute

// failure is a remembered discovery error and when it was recorded.
type failure struct {
	err error
	at  time.Time
}

// NewResolver builds a resolver over a loaded config. cat may be nil, which
// disables catalog-backed discovery (paseo still answers natively).
func NewResolver(cfg *config.Config, cat *Catalog) *Resolver {
	return &Resolver{
		cfg: cfg, cat: cat,
		rosters: map[string]Roster{},
		failed:  map[string]failure{},
	}
}

// clock is now, defaulted.
func (r *Resolver) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// ErrNoAcceptableModel is the rung-4 hard error: a `required: true` fleet with
// nothing acceptable in any roster.
var ErrNoAcceptableModel = errors.New("no acceptable model")

// Resolve walks the ladder for one step. runtimeHint pins the search to a
// single `runtimes:` entry (a step's own `runtime:`); empty considers every
// configured runtime.
func (r *Resolver) Resolve(ctx context.Context, spec config.ModelSpec, runtimeHint string) (Decision, error) {
	rts := r.agentRuntimes(runtimeHint)

	// No `model:` at all — the runtime's own default, then its best available
	// prefer: entry, then bare launch (§3.2).
	if !spec.Set() {
		return r.runtimeDefault(ctx, rts), nil
	}

	fleetName, resolved := r.deref(spec)

	// 1. Consumer override.
	if fleetName != "" {
		if over, ok := r.Overrides[fleetName]; ok && strings.TrimSpace(over) != "" {
			rt := r.runtimeOffering(ctx, over, rts)
			return Decision{
				Model: over, Runtime: rt, Provider: r.providerFor(ctx, rt, over),
				Reason: fmt.Sprintf("consumer override of fleet %q", fleetName),
			}, nil
		}
	}

	acceptable := resolved.Acceptable()
	if len(acceptable) == 0 {
		return r.runtimeDefault(ctx, rts), nil
	}

	// 2. Intersect with the union of the rosters, ranked by prefer:.
	if best, rt, prov, ok := r.bestAcceptable(ctx, acceptable, rts); ok {
		return Decision{Model: best, Runtime: rt, Provider: prov, Reason: r.reasonFor(fleetName, acceptable)}, nil
	}

	// An EXACT PIN nothing could confirm or deny passes through. When no
	// runtime can enumerate, conductor is not the authority on what exists —
	// refusing an operator's explicit model because discovery is unavailable
	// would make an offline box strictly less capable than before fleets
	// existed. (When a runtime CAN enumerate and says no, we fall through to
	// rung 3/4 instead — that is a real answer.)
	if pin, ok := exactPin(acceptable); ok && !r.anyRuntimeEnumerates(ctx, rts) {
		return Decision{
			Model:  pin,
			Reason: "exact pin (no runtime could enumerate to confirm it)",
		}, nil
	}

	available := r.unionRoster(ctx, rts)
	why := r.discoveryErrors(ctx, rts)
	// 4. required: true and nothing matched — a hard error naming both sides.
	if resolved.Required {
		return Decision{}, fmt.Errorf("%w for %s: acceptable %s; available %s",
			ErrNoAcceptableModel, fleetLabel(fleetName), strings.Join(acceptable, ", "), availableLabelWhy(available, why))
	}
	// 3. required: false — bare launch, with a notice so the fall-through is
	// visible rather than silent.
	prov, provWhere := r.bareProvider(ctx, rts)
	return Decision{
		Bare:     true,
		Provider: prov,
		Reason:   "no acceptable model available; required: false",
		Notice: fmt.Sprintf("%s matched nothing available (acceptable %s; available %s) — dispatching bare%s",
			fleetLabel(fleetName), strings.Join(acceptable, ", "), availableLabelWhy(available, why), bareSuffix(prov, provWhere)),
	}, nil
}

// bareProvider is the provider to name on a bare launch: the first provider
// the discovered roster reports. Returns it and where it came from, for the
// notice.
//
// A bare launch means "no --model, let the runtime pick" — it does NOT have
// to mean "no --provider". paseo rejects a run that names neither, so an
// unqualified bare launch is not a degrade on that backend, it is an outage.
// Naming a provider keeps the fallback actually launchable while still
// leaving the model choice to the runtime.
//
// It is derived, never configured. A `models.provider:` key was considered and
// dropped: it would be permanent config surface for a path a healthy box never
// takes, and the case it uniquely covers — discovery down AND a provider
// pinned — is better served by fixing discovery (§4.3's ladder) than by
// hand-maintaining a fallback that is only consulted when something is already
// wrong.
func (r *Resolver) bareProvider(ctx context.Context, rts []string) (provider, where string) {
	for _, name := range rts {
		for _, m := range r.allowedRoster(ctx, name) {
			if m.Provider != "" {
				return m.Provider, "first available provider on runtime " + name
			}
		}
	}
	return "", ""
}

func bareSuffix(provider, where string) string {
	if provider == "" {
		return " (the runtime's own default)"
	}
	return fmt.Sprintf(" on provider %q (%s) — the runtime picks the model", provider, where)
}

// deref applies the map-key-wins rule to the string form: a `model:` string
// that names a key in the top-level `models:` block is a FLEET REFERENCE;
// anything else is a model id or a wildcard. Returns the fleet name (empty
// when the spec was not a fleet reference) and the spec to resolve.
func (r *Resolver) deref(spec config.ModelSpec) (fleet string, resolved config.ModelSpec) {
	if spec.Ref == "" {
		return "", spec
	}
	if f, ok := r.cfg.Models[spec.Ref]; ok {
		return spec.Ref, f
	}
	return "", spec
}

// bestAcceptable performs rung 2: expand the acceptable list against each
// candidate runtime's (allow-filtered) roster, then pick the winner.
//
// Ranking, in order:
//
//   - the fleet's own ordering (`any:` is the author's ranking, and a
//     wildcard expands in roster order — newest-first);
//   - overlaid by the CONSUMER's prefer: (the consumer disposes) — a
//     candidate's rank is the best prefer-index among the runtimes offering
//     it;
//   - ties on the runtime side broken by `default: true`, then name order
//     (a YAML map has no declaration order to appeal to, so name order is the
//     deterministic stand-in).
func (r *Resolver) bestAcceptable(ctx context.Context, acceptable []string, rts []string) (model, runtime, provider string, ok bool) {
	ranked := r.rankAcceptable(ctx, acceptable, rts)
	if len(ranked) == 0 {
		return "", "", "", false
	}
	return ranked[0].model, ranked[0].runtime, ranked[0].provider, true
}

// rankedModel is one acceptable model with the runtime that should run it.
type rankedModel struct {
	model    string
	fleetPos int
	rank     int
	runtime  string
	provider string
}

// rankAcceptable is rung 2's full ordering: every acceptable model any
// candidate runtime offers, best first, each on its best runtime. See
// bestAcceptable for the ranking.
func (r *Resolver) rankAcceptable(ctx context.Context, acceptable []string, rts []string) []rankedModel {
	type cand = rankedModel
	best := map[string]*cand{}
	var order []string

	for _, name := range rts {
		roster := r.allowedRoster(ctx, name)
		if len(roster) == 0 {
			continue
		}
		prefer := r.preferOf(name)
		expanded := config.ExpandModelPatterns(acceptable, roster.IDs())
		for pos, m := range expanded {
			if r.Excluded != nil && r.Excluded(name, m) {
				continue // marked unsupported on this runtime — next candidate
			}
			rank := preferRank(m, prefer)
			c, seen := best[m]
			if !seen {
				best[m] = &cand{model: m, fleetPos: pos, rank: rank, runtime: name, provider: providerOf(roster, m)}
				order = append(order, m)
				continue
			}
			// Same model on several runtimes: keep the better prefer rank,
			// then the better runtime.
			if rank < c.rank || (rank == c.rank && r.runtimeBeats(name, c.runtime)) {
				c.rank, c.runtime, c.provider = rank, name, providerOf(roster, m)
			}
			if pos < c.fleetPos {
				c.fleetPos = pos
			}
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := best[order[i]], best[order[j]]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		return a.fleetPos < b.fleetPos
	})
	out := make([]rankedModel, len(order))
	for i, m := range order {
		out[i] = *best[m]
	}
	return out
}

// providerOf looks up model's provider in an already-discovered roster,
// confirming the pick — "" when the roster doesn't carry it (or doesn't say).
func providerOf(roster Roster, model string) string {
	if m, ok := roster.Find(model); ok {
		return m.Provider
	}
	return ""
}

// providerFor is providerOf, discovering runtime's roster first. "" when
// runtime is unset (nothing confirmed the pick) or the roster doesn't carry
// the model.
func (r *Resolver) providerFor(ctx context.Context, runtime, model string) string {
	if runtime == "" || model == "" {
		return ""
	}
	return providerOf(r.allowedRoster(ctx, runtime), model)
}

// runtimeDefault answers a step that named no model: the runtime's explicit
// models.default:, then its first AVAILABLE prefer: entry, then bare launch.
func (r *Resolver) runtimeDefault(ctx context.Context, rts []string) Decision {
	for _, name := range rts {
		rt := r.cfg.Runtimes[name]
		if rt.Models == nil {
			continue
		}
		if rt.Models.Default != "" {
			return Decision{Model: rt.Models.Default, Runtime: name,
				Provider: r.providerFor(ctx, name, rt.Models.Default),
				Reason:   "runtime " + name + " models.default"}
		}
	}
	// A resolved prefer: hit is the effective default when nothing is
	// declared (§3.2) — but only if the roster confirms it.
	for _, name := range rts {
		prefer := r.preferOf(name)
		if len(prefer) == 0 {
			continue
		}
		roster := r.allowedRoster(ctx, name)
		if hit := config.ExpandModelPatterns(prefer, roster.IDs()); len(hit) > 0 {
			return Decision{Model: hit[0], Runtime: name, Provider: providerOf(roster, hit[0]),
				Reason: "runtime " + name + " models.prefer"}
		}
	}
	// Bare launch: nothing declared and no prefer: confirmed. Name a provider
	// if we can (see bareProvider) so the launch is actually valid on a
	// runtime that requires one; only when we cannot does this stay a true
	// bare launch, and the notice then says so — a paseo runtime reaching
	// here with no provider WILL fail with MISSING_PROVIDER, and that
	// ahead-of-time hint is the whole point of the notice (#12092).
	prov, provWhere := r.bareProvider(ctx, rts)
	if prov != "" {
		return Decision{Bare: true, Provider: prov,
			Reason: "no model declared — bare launch on provider " + prov,
			Notice: fmt.Sprintf("no model declared for this runtime — dispatching bare on provider %q (%s); the runtime picks the model", prov, provWhere)}
	}
	return Decision{Bare: true,
		Reason: "no model declared — bare launch (the runtime's own default)",
		Notice: "no model declared for this runtime and no provider could be derived — dispatching bare; the runtime must supply its own default (a paseo runtime with no provider will fail with MISSING_PROVIDER — set `model:` or `models.default:`)"}
}

// candidateRuntimes is the ordered set of runtimes to search: the hinted one
// alone, else every configured runtime with `default: true` first and the
// rest by name.
func (r *Resolver) candidateRuntimes(hint string) []string {
	if hint != "" {
		if _, ok := r.cfg.Runtimes[hint]; ok {
			return []string{hint}
		}
		// A hint naming a legacy controllers: entry (or nothing at all) has no
		// runtimes: roster to search; validation reports an unknown name.
		return nil
	}
	names := make([]string, 0, len(r.cfg.Runtimes))
	for n := range r.cfg.Runtimes {
		names = append(names, n)
	}
	sort.Strings(names)
	sort.SliceStable(names, func(i, j int) bool {
		return r.cfg.Runtimes[names[i]].Default && !r.cfg.Runtimes[names[j]].Default
	})
	return names
}

// agentRuntimes is candidateRuntimes without the decision-only runtimes —
// the set an agent step (and the agent path of a decide step) may run on.
func (r *Resolver) agentRuntimes(hint string) []string {
	all := r.candidateRuntimes(hint)
	if r.DecisionOnly == nil {
		return all
	}
	out := all[:0:0]
	for _, name := range all {
		if !r.DecisionOnly(name) {
			out = append(out, name)
		}
	}
	return out
}

// nativeRuntimes is the candidate runtimes that answer protocol natively.
func (r *Resolver) nativeRuntimes(hint, protocol string) []string {
	if r.NativeProtocols == nil {
		return nil
	}
	var out []string
	for _, name := range r.candidateRuntimes(hint) {
		for _, p := range r.NativeProtocols(name) {
			if p == protocol {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// Candidate is one (runtime, model) pair that can answer a decide: step.
type Candidate struct {
	// Model is the model id; empty is a bare launch on an agent runtime.
	Model string
	// Runtime is the runtimes: entry that runs it. Empty only for an agent
	// candidate no roster confirmed (a pass-through pin, a bare launch with
	// no runtime pick) — the step's default runtime then runs it.
	Runtime  string
	Provider string
	// Native marks a runtime that answers the protocol itself. Otherwise the
	// candidate is an agent runtime answering through conductor's adapter.
	Native bool
	Bare   bool
}

// Label is "<runtime>/<model>", the `_by` a decide step records.
func (c Candidate) Label() string {
	m := c.Model
	if m == "" {
		m = "default"
	}
	rt := c.Runtime
	if rt == "" {
		rt = "default"
	}
	return rt + "/" + m
}

// Candidates ranks every (runtime, model) pair that can answer a decide:
// step speaking protocol, for the step's model: spec. Native runtimes come
// first — they answer the protocol directly — then agent runtimes, each group
// in rung-2 order (fleet order overlaid by prefer:). The agent group falls
// back exactly as Resolve does (runtime default, exact-pin pass-through,
// bare launch), so a decide step always has at least one candidate unless a
// required: true fleet matched nothing anywhere.
//
// A native runtime is never offered for a step with no model: at all — it
// is reached only when a fleet (or a pin) names its models, which is the
// consumer's opt-in.
func (r *Resolver) Candidates(ctx context.Context, spec config.ModelSpec, runtimeHint, protocol string) ([]Candidate, error) {
	var native []Candidate
	if spec.Set() {
		fleetName, resolved := r.deref(spec)
		acceptable := resolved.Acceptable()
		if fleetName != "" {
			if over, ok := r.Overrides[fleetName]; ok && strings.TrimSpace(over) != "" {
				acceptable = []string{over}
			}
		}
		for _, m := range r.rankAcceptable(ctx, acceptable, r.nativeRuntimes(runtimeHint, protocol)) {
			native = append(native, Candidate{Model: m.model, Runtime: m.runtime, Provider: m.provider, Native: true})
		}
	}

	var agent []Candidate
	agentRts := r.agentRuntimes(runtimeHint)
	if spec.Set() {
		fleetName, resolved := r.deref(spec)
		acceptable := resolved.Acceptable()
		overridden := false
		if fleetName != "" {
			if over, ok := r.Overrides[fleetName]; ok && strings.TrimSpace(over) != "" {
				acceptable, overridden = []string{over}, true
			}
		}
		if !overridden {
			for _, m := range r.rankAcceptable(ctx, acceptable, agentRts) {
				agent = append(agent, Candidate{Model: m.model, Runtime: m.runtime, Provider: m.provider})
			}
		}
	}
	if len(agent) == 0 && len(agentRts) > 0 {
		// Nothing ranked on an agent runtime: take the one decision Resolve
		// would make (override, default, pass-through pin, bare launch).
		d, err := r.Resolve(ctx, spec, runtimeHint)
		switch {
		case err != nil && len(native) == 0:
			return nil, err
		case err == nil:
			// A fleet that only native runtimes satisfy resolves, on the
			// agent side, to a bare launch — which is still a valid last
			// resort, so it stays in the list after the native candidates.
			// A pinned runtime is where it runs even when no roster named it
			// (a bare launch carries no runtime of its own).
			rt := d.Runtime
			if rt == "" && runtimeHint != "" {
				rt = runtimeHint
			}
			agent = append(agent, Candidate{Model: d.Model, Runtime: rt, Provider: d.Provider, Bare: d.Bare})
		}
	}
	return append(native, agent...), nil
}

// runtimeBeats reports whether runtime a should win a tie over b.
func (r *Resolver) runtimeBeats(a, b string) bool {
	ra, rb := r.cfg.Runtimes[a], r.cfg.Runtimes[b]
	if ra.Default != rb.Default {
		return ra.Default
	}
	return a < b
}

// runtimeOffering names the runtime whose roster carries a model, or "" when
// none does (an override the operator is asserting).
func (r *Resolver) runtimeOffering(ctx context.Context, model string, rts []string) string {
	for _, name := range rts {
		if _, ok := r.allowedRoster(ctx, name).Find(model); ok {
			return name
		}
	}
	return ""
}

func (r *Resolver) preferOf(name string) []string {
	if m := r.cfg.Runtimes[name].Models; m != nil {
		return m.Prefer
	}
	return nil
}

// allowedRoster is a runtime's discovered roster with its `allow:` applied.
func (r *Resolver) allowedRoster(ctx context.Context, name string) Roster {
	full := r.roster(ctx, name)
	rt := r.cfg.Runtimes[name]
	if rt.Models == nil || len(rt.Models.Allow) == 0 {
		return full
	}
	keep := config.FilterRoster(full.IDs(), rt.Models.Allow)
	allowed := make(map[string]bool, len(keep))
	for _, id := range keep {
		allowed[id] = true
	}
	out := make(Roster, 0, len(keep))
	for _, m := range full {
		if allowed[m.ID] {
			out = append(out, m)
		}
	}
	return out
}

// roster discovers one runtime's models, memoized (including the failure, so
// an unreachable provider is not re-probed for every dispatch in a fan-out).
//
// Concurrent callers for the SAME runtime share one discovery. The cache
// alone was not enough: it is consulted and populated under the lock but
// List runs outside it, so a `parallel:` step whose branches all dispatch
// at once had every branch probing the provider simultaneously — N cold
// round-trips, and N chances to trip a rate limit — before the first
// result was cached.
func (r *Resolver) roster(ctx context.Context, name string) Roster {
	for {
		r.mu.Lock()
		if cached, ok := r.rosters[name]; ok {
			r.mu.Unlock()
			return cached
		}
		if f, bad := r.failed[name]; bad {
			if r.clock().Sub(f.at) < FailureTTL {
				r.mu.Unlock()
				return nil
			}
			// Expired — forget it and probe again below.
			delete(r.failed, name)
		}
		if wait, inflight := r.inflight[name]; inflight {
			// Someone else is already asking. Wait for their answer
			// rather than asking again.
			r.mu.Unlock()
			<-wait
			continue
		}
		done := make(chan struct{})
		if r.inflight == nil {
			r.inflight = map[string]chan struct{}{}
		}
		r.inflight[name] = done
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			delete(r.inflight, name)
			r.mu.Unlock()
			close(done)
		}()
		break
	}

	rt, ok := r.cfg.Runtimes[name]
	if !ok {
		return nil
	}
	lister, ok := ListerFor(runtimeOf(name, rt), r.cat)
	if !ok {
		r.mu.Lock()
		r.failed[name] = failure{err: ErrNoDiscovery, at: r.clock()}
		r.mu.Unlock()
		return nil
	}
	// The shared discovery runs on a context derived from the BACKGROUND
	// one, carrying only this call's deadline-free cancellation semantics.
	// A leader whose caller had a 1ns deadline would otherwise fail, cache
	// its own ctx error durably, and bare-launch every dispatch after it —
	// one unlucky caller poisoning the runtime forever. Followers waiting
	// on this leader are not that caller either.
	roster, err := lister.List(context.WithoutCancel(ctx))
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		// A cancellation or deadline is about the CALLER, not the runtime.
		// Remembering it as "this runtime cannot enumerate" is how a
		// transient timeout became permanent.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		r.failed[name] = failure{err: err, at: r.clock()}
		return nil
	}
	r.rosters[name] = roster
	return roster
}

// unionRoster is every candidate runtime's allow-filtered roster, deduped —
// the "available" half of a rung-4 error message.
func (r *Resolver) unionRoster(ctx context.Context, rts []string) Roster {
	var out Roster
	for _, name := range rts {
		out = append(out, r.allowedRoster(ctx, name)...)
	}
	return out.Dedupe()
}

// anyRuntimeEnumerates reports whether at least one candidate runtime
// produced a roster — i.e. whether "not available" is a real answer or just
// an absence of information.
func (r *Resolver) anyRuntimeEnumerates(ctx context.Context, rts []string) bool {
	for _, name := range rts {
		if len(r.roster(ctx, name)) > 0 {
			return true
		}
	}
	return false
}

// Rosters returns each candidate runtime's allow-filtered roster, for
// `conductor validate` and diagnostics.
func (r *Resolver) Rosters(ctx context.Context) map[string]Roster {
	out := map[string]Roster{}
	for _, name := range r.candidateRuntimes("") {
		out[name] = r.allowedRoster(ctx, name)
	}
	return out
}

// runtimeOf projects a config runtime entry onto the discovery view.
func runtimeOf(name string, rt config.RuntimeConfig) Runtime {
	impl := ""
	if u, err := rt.Resolved(); err == nil {
		impl = u.Name
	}
	return Runtime{
		Name: name, Impl: impl, Bin: rt.Bin,
		Home: rt.Home, Server: rt.Server, Remote: rt.Host != "",
		Agent: rt.Agent, Tool: rt.Tool, Command: rt.Command,
	}
}

// exactPin reports the single literal model an acceptable list names, if that
// is all it names.
func exactPin(acceptable []string) (string, bool) {
	if len(acceptable) != 1 || config.IsModelPattern(acceptable[0]) {
		return "", false
	}
	return acceptable[0], true
}

func preferRank(model string, prefer []string) int {
	for i, p := range prefer {
		if config.MatchModelPattern(p, model) {
			return i
		}
	}
	return len(prefer)
}

func (r *Resolver) reasonFor(fleet string, acceptable []string) string {
	if fleet != "" {
		return "fleet " + fleet
	}
	if pin, ok := exactPin(acceptable); ok {
		return "exact model " + pin
	}
	return "inline fleet"
}

func fleetLabel(fleet string) string {
	if fleet == "" {
		return "the step's model:"
	}
	return fmt.Sprintf("fleet %q", fleet)
}

func availableLabel(r Roster) string {
	return availableLabelWhy(r, nil)
}

// availableLabelWhy is availableLabel plus the discovery errors behind an
// empty roster. "no runtime could enumerate" on its own told an operator that
// something was wrong but never what — the reason (a daemon not running, a
// wrong home, a rate limit) was recorded and then discarded.
func availableLabelWhy(r Roster, why []string) string {
	if len(r) > 0 {
		return strings.Join(r.IDs(), ", ")
	}
	if len(why) == 0 {
		return "(none — no configured runtime could enumerate its models)"
	}
	return "(none — no configured runtime could enumerate its models: " + strings.Join(why, "; ") + ")"
}

// discoveryErrors reports why each candidate runtime failed to enumerate, for
// the diagnostic half of a bare-launch notice or a rung-4 error.
func (r *Resolver) discoveryErrors(ctx context.Context, rts []string) []string {
	var out []string
	for _, name := range rts {
		r.roster(ctx, name) // ensure probed
		r.mu.Lock()
		f, bad := r.failed[name]
		r.mu.Unlock()
		if bad && f.err != nil {
			out = append(out, name+": "+f.err.Error())
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Load-time checking
// ---------------------------------------------------------------------------

// CheckRequired walks every `required: true` fleet a step references and
// reports the first that nothing can satisfy — rung 4, surfaced up front
// rather than at the first live trigger.
//
// It errors ONLY when discovery actually answered. A box that cannot reach
// its providers has not learned that a model is unavailable; it has learned
// nothing, and turning that into a hard failure would crash-loop an
// auto-updating fleet on the first network blip.
//
// It is called from `conductor validate` ONLY — where an operator is
// present to read the error and fix the config. Dispatch deliberately does
// NOT call it: there the same unsatisfiable fleet logs and bare-launches,
// because a box that stops taking work is worse than one taking it on the
// wrong model. config.Load never calls it either; loading must stay
// offline. (The comment here previously claimed dispatch called it. It did
// not — nothing did.)
func (r *Resolver) CheckRequired(ctx context.Context) error {
	for _, ref := range r.cfg.ModelRefs() {
		_, resolved := r.deref(ref.Spec)
		if !resolved.Required {
			continue
		}
		if !r.anyRuntimeEnumerates(ctx, r.candidateRuntimes(ref.Runtime)) {
			continue // no information, not a negative answer
		}
		if _, err := r.Resolve(ctx, ref.Spec, ref.Runtime); err != nil {
			return fmt.Errorf("config: %s: %w", ref.Where, err)
		}
	}
	return nil
}
