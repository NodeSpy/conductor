package models

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

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

	mu      sync.Mutex
	rosters map[string]Roster
	failed  map[string]error
	// inflight dedups concurrent cold discovery per runtime: the second
	// caller waits on the first's channel instead of issuing its own
	// List. See roster.
	inflight map[string]chan struct{}
}

// NewResolver builds a resolver over a loaded config. cat may be nil, which
// disables catalog-backed discovery (paseo still answers natively).
func NewResolver(cfg *config.Config, cat *Catalog) *Resolver {
	return &Resolver{
		cfg: cfg, cat: cat,
		rosters: map[string]Roster{},
		failed:  map[string]error{},
	}
}

// ErrNoAcceptableModel is the rung-4 hard error: a `required: true` fleet with
// nothing acceptable in any roster.
var ErrNoAcceptableModel = errors.New("no acceptable model")

// Resolve walks the ladder for one step. runtimeHint pins the search to a
// single `runtimes:` entry (a step's own `runtime:`); empty considers every
// configured runtime.
func (r *Resolver) Resolve(ctx context.Context, spec config.ModelSpec, runtimeHint string) (Decision, error) {
	rts := r.candidateRuntimes(runtimeHint)

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
				Model: over, Runtime: rt,
				Reason: fmt.Sprintf("consumer override of fleet %q", fleetName),
			}, nil
		}
	}

	acceptable := resolved.Acceptable()
	if len(acceptable) == 0 {
		return r.runtimeDefault(ctx, rts), nil
	}

	// 2. Intersect with the union of the rosters, ranked by prefer:.
	if best, rt, ok := r.bestAcceptable(ctx, acceptable, rts); ok {
		return Decision{Model: best, Runtime: rt, Reason: r.reasonFor(fleetName, acceptable)}, nil
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
	// 4. required: true and nothing matched — a hard error naming both sides.
	if resolved.Required {
		return Decision{}, fmt.Errorf("%w for %s: acceptable %s; available %s",
			ErrNoAcceptableModel, fleetLabel(fleetName), strings.Join(acceptable, ", "), availableLabel(available))
	}
	// 3. required: false — bare launch, with a notice so the fall-through is
	// visible rather than silent.
	return Decision{
		Bare:   true,
		Reason: "no acceptable model available; required: false",
		Notice: fmt.Sprintf("%s matched nothing available (acceptable %s; available %s) — dispatching bare (the runtime's own default)",
			fleetLabel(fleetName), strings.Join(acceptable, ", "), availableLabel(available)),
	}, nil
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
func (r *Resolver) bestAcceptable(ctx context.Context, acceptable []string, rts []string) (model, runtime string, ok bool) {
	type cand struct {
		model    string
		fleetPos int
		rank     int
		runtime  string
	}
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
			rank := preferRank(m, prefer)
			c, seen := best[m]
			if !seen {
				best[m] = &cand{model: m, fleetPos: pos, rank: rank, runtime: name}
				order = append(order, m)
				continue
			}
			// Same model on several runtimes: keep the better prefer rank,
			// then the better runtime.
			if rank < c.rank || (rank == c.rank && r.runtimeBeats(name, c.runtime)) {
				c.rank, c.runtime = rank, name
			}
			if pos < c.fleetPos {
				c.fleetPos = pos
			}
		}
	}
	if len(order) == 0 {
		return "", "", false
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := best[order[i]], best[order[j]]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		return a.fleetPos < b.fleetPos
	})
	w := best[order[0]]
	return w.model, w.runtime, true
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
			return Decision{Model: rt.Models.Default, Runtime: name, Reason: "runtime " + name + " models.default"}
		}
	}
	// A resolved prefer: hit is the effective default when nothing is
	// declared (§3.2) — but only if the roster confirms it.
	for _, name := range rts {
		prefer := r.preferOf(name)
		if len(prefer) == 0 {
			continue
		}
		if hit := config.ExpandModelPatterns(prefer, r.allowedRoster(ctx, name).IDs()); len(hit) > 0 {
			return Decision{Model: hit[0], Runtime: name, Reason: "runtime " + name + " models.prefer"}
		}
	}
	return Decision{Bare: true, Reason: "no model declared — bare launch (the runtime's own default)"}
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
		if _, bad := r.failed[name]; bad {
			r.mu.Unlock()
			return nil
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
		r.failed[name] = ErrNoDiscovery
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
		r.failed[name] = err
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
	if len(r) == 0 {
		return "(none — no configured runtime could enumerate its models)"
	}
	return strings.Join(r.IDs(), ", ")
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
