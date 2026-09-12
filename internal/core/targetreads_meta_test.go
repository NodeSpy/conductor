package core

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// THE ENFORCEMENT (round-13). Six rounds of review found the same bug six
// times: broker keys, session binding, memory own-scope, the outcome store,
// prompt recall, affinity key rendering — each one a consumer reading the
// dispatch target RAW and deciding something with it, each one found by a
// human reading code rather than by a test.
//
// core.OwnRepo and Trigger.Key exist and are correct. What did not exist was
// anything making a consumer USE them, so every round found the next consumer
// that hadn't. Point-fixing does not converge on a codebase that keeps
// growing consumers.
//
// So: every read of the dispatch target in the tree is enumerated here, and
// each one is either
//
//	FIXED     — routed through the trusted accessor (OwnRepo/Key), and so no
//	            longer a raw read at all, or
//	EXCEPTED  — listed below with a REASON, because it feeds logging, display,
//	            checkout, or template data rather than a decision.
//
// A new raw read in a function that is not on the list fails this test with
// its file and line. The author then has one of two jobs: route it through
// the accessor, or add it here with the sentence explaining why a forged
// target is harmless there. Both are cheap; neither is silent.
//
// SCOPE, stated honestly: this matches `.Target.<field>` syntactically, which
// is where all six findings lived, plus the `.Source.Repo` provenance
// spelling. It does not type-check, so a target copied into a local variable
// first and read later escapes it. That is a real limit, not a claimed one —
// the test buys "a new consumer cannot read the target directly without being
// noticed", which is the shape every finding so far had.

// targetFields are the dispatch-target reads that have ever fed a decision.
var targetFields = map[string]bool{
	"Repo": true, "Owner": true, "Name": true,
	"Number": true, "PR": true, "Issue": true,
}

// rawTargetException is one audited raw read: the function it lives in, and
// why a forged target cannot hurt there.
type rawTargetException struct {
	fn     string
	reason string
}

// rawTargetExceptions are the reads that may stay raw, by file and enclosing
// function. Every entry answers one question: if the event's SENDER chose
// this repo, what goes wrong here? "Nothing — it is only displayed/logged/
// cloned/templated" is the only acceptable answer.
var rawTargetExceptions = map[string][]rawTargetException{
	// ---- core itself: the accessors and the key are where the rule LIVES.
	"internal/core/event.go": {
		{"OwnRepo", "the accessor that IS the rule"},
		{"Key", "builds the key; consults OwnRepo first and namespaces an untrusted target"},
	},

	// ---- Display, logs, audit rows, notifications.
	"internal/flow/flow.go": {
		{"flowTag", "a log prefix"},
		{"auditRunCost", "an audit row"},
		{"harvestMemory", "the memory Source's provenance; the harvest's scope check goes through NewAgentCaller"},
		{"execVerb", "audit + rendered options; the resource checks in this function go through OwnRepo"},
		{"runSteps", "step audit rows"},
		{"auditVerb", "an audit row records what was ATTEMPTED, forged target included — that is the point of an audit"},
		{"audit", "as above"},
		{"stepAudit", "as above"},
		{"execAgent", "template data for the agent's prompt + audit; the authz decisions in this function go through OwnRepo"},
		{"runHooks", "template data"},
		{"Run", "run bookkeeping + the memory Source it stamps, which carries TargetTrusted alongside"},
		{"dispatchAgent", "template data and labels"},
	},
	"internal/flow/template.go": {
		{"baseData", "TEMPLATE DATA for prompts and options. Not an authz input: the scope layer builds its own facts (scopeRenderData) and deliberately does not use this"},
	},
	"internal/flow/skillverbs.go": {
		{"unattributedAgent", "an audit attribution string for a step with no name"},
		{"auditSkillVerb", "an audit row"},
		{"RunSkillVerb", "rebuilds the trigger it then passes to the checks, which call OwnRepo themselves; the Caller it builds goes through NewAgentCaller"},
	},
	"internal/flow/group.go": {
		{"groupKeyFor", "delegates to Trigger.Key, which is trust-aware"},
	},
	"internal/engine/engine.go": {
		{"logf", "a log prefix"},
		{"tag", "a log prefix"},
		{"auditDispatch", "an audit row"},
		{"notifyTarget", "a notification's display fields"},
		{"dispatchOne", "labels, display, and the memory Source it stamps (which carries TargetTrusted)"},
		{"harvestMemory", "audit + the memory Source's provenance; the harvest's scope check goes through NewAgentCaller"},
		{"process", "audit rows; the dedup/attempt records in this function use Trigger.Key()"},
		{"newRun", "the persisted run record's display fields"},
		{"recoverDispatch", "a panic-path audit row"},
		{"ResumeWorkflows", "a resume audit row"},
		{"workflowRunStatus", "the gh status query for the run the trigger already named"},
		{"memoryPrompt", "recall now goes through OwnRepo(); the remaining read is the workflow/identity key convention"},
	},
	"internal/engine/outcome.go": {
		{"observeOutcomeSignals", "the gate is the first line (TargetTrusted); the keys go through Trigger.Key()"},
		{"observeClosed", "reached only from a TRUSTED-target _closed (observeOutcomeSignals refuses the rest); the repo it reads is therefore platform-assigned, and the engagement key goes through Trigger.Key()"},
		{"recordDecisionOutcome", "an audit row for a decision already taken"},
	},
	"internal/notify/notify.go": {
		{"Emit", "the notification body — a human reads it, nothing branches on it"},
		{"send", "as above"},
		{"fields", "as above"},
	},
	"internal/dispatch/toolserver.go": {
		{"BuildToolServer", "argv provenance for the tool subprocess; the trust bit travels beside it and the socket resolves authorization from the credential, not from these"},
		{"SkillEnv", "as above"},
	},
	"internal/dispatch/ghwrite.go": {
		{"Comment", "the github API call's own target — the write goes where the dispatch says, which is what a forged target's OWN repo is"},
	},
	"internal/controller/runner.go": {
		{"runKey", "delegates to Trigger.Key"},
	},
	"internal/store/history.go": {
		{"record", "the run history record's display fields"},
	},
	"internal/inbound/target.go": {
		{"SyntheticTarget", "CONSTRUCTS a target; it reads nothing"},
	},

	// ---- Guarded reads: the function asks OwnRepo/TargetTrusted FIRST and
	// only then reads the rest of the target. The raw read is inside the
	// guard, which is the shape we want, not the one we are hunting.
	"internal/connector/scope.go": {
		{"ContextScope", "reads Target.Repo only on the !TargetTrusted-checked branch"},
	},
	"internal/flow/scoperender.go": {
		{"scopeRenderData", "derives owner/name/number only when OwnRepo() is non-empty"},
	},
	"internal/flow/handles.go": {
		{"agentAuthoredNamespace", "reads Target.Number only inside the OwnRepo() != \"\" branch"},
	},

	// ---- Template DATA for prompts, commands and options. An agent is TOLD
	// which repo its dispatch names; that is not an authorization input, and
	// the scope layer deliberately builds its own facts instead of reusing
	// these (scopeRenderData).
	"internal/engine/flow.go": {
		{"flowBaseData", "template data"},
		{"newFlowRun", "run record display fields"},
		{"resumeFlowRun", "as above"},
		{"flowAgentServices", "passes the trigger through to the flow runner, which does its own checks"},
	},
	"internal/engine/steps.go": {
		{"runSteps", "audit rows and step template data"},
		{"stepBaseData", "template data"},
		{"startReviewHandoff", "the hand-off DRAFT's display fields; its broker binding uses Trigger.Key()"},
	},
	"internal/dispatch/dispatch.go": {
		{"templateData", "the prompt/command template scope. RenderSessionKey builds the trusted view for identity rendering; this one is display"},
	},
	"internal/flow/plan.go": {
		{"executePlan", "audit + plan template data"},
		{"resumePlan", "as above"},
		{"auditPlan", "an audit row"},
		{"compensatePlan", "an audit row"},
		{"RunLiveStep", "RECONSTRUCTS a trigger from provenance, carrying TargetTrusted and DispatchID with it"},
	},
	"internal/flow/supervise.go": {
		{"approvePlan", "the approval hand-off's display fields"},
		{"planCheckIn", "as above"},
		{"planStepFailed", "as above"},
	},
	"internal/flow/team.go": {
		{"execTeam", "worker template data and audit"},
	},
	"internal/flow/gate.go": {
		{"runGate", "gate-check template data and audit"},
		{"escalateGate", "the escalation notification"},
	},
	"internal/flow/wfverbs.go": {
		{"workflowRun", "the called workflow's template data"},
		{"workflowSave", "an audit row"},
	},
	"internal/flow/history.go": {
		{"beginHistory", "the run history record's display fields"},
	},
	"internal/flow/budget.go": {
		{"recordBackgroundEstimate", "an audit row"},
	},

	// ---- Audit rows and logs.
	"internal/engine/budget.go": {
		{"shedForBudget", "an audit row; the attempt record it writes uses Trigger.Key()"},
		{"recordUsage", "an audit row"},
	},
	"internal/memory/ipc.go": {
		{"handleIPC", "audit rows; the Caller it authorizes with goes through NewAgentCaller, and provenance comes from the credential"},
	},
	"internal/memory/harvest.go": {
		{"HarvestOutput", "the Caller goes through NewAgentCaller; the raw read is provenance recorded on the entry"},
	},
	"internal/connector/conductor.go": {
		{"EmitLifecycle", "the lifecycle event's display context"},
	},
	"internal/controller/agentdeck.go": {
		{"deckTitle", "a session title"},
		{"deckGroup", "a display grouping in the deck UI"},
	},
	"internal/controller/opencode.go": {
		{"opencodeTitle", "a session title"},
	},
	"internal/integrations/github/events.go": {
		{"triggersFor", "CONSTRUCTS the trigger from the verified payload"},
	},

	// ---- The checkout. paseo is told which repo to clone and which PR to
	// check out; a forged target clones the attacker's own repo into the
	// agent's sandbox, which is the sandbox doing its job.
	"internal/dispatch/paseo.go": {
		{"repoStrategy", "checkout strategy for the clone"},
		{"checkoutArgs", "the clone/checkout arguments"},
		{"createWorktree", "the worktree path for that checkout"},
		{"branchSlug", "a branch name for the work"},
		{"labelArgs", "display labels; the PR label goes through Trigger.Key()"},
		{"adoptAgentForBranch", "matches the branch label written above"},
	},

	// ---- CLI display.
	"cmd/conductor/main.go": {
		{"cmdReplay", "prints a stored trigger for the operator"},
		{"printTrigger", "as above"},
	},
	"cmd/conductor/workflows.go": {
		{"cmdWorkflows", "prints a run listing"},
	},
}

func TestEveryDispatchTargetReadIsAuditedOrRouted(t *testing.T) {
	root := repoRootFor(t)
	type read struct {
		file, fn string
		line     int
	}
	var flagged []read
	seen := map[string]bool{}

	err := filepath.Walk(root, func(path string, fi os.FileInfo, werr error) error {
		if werr != nil || fi.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "test/") || strings.HasPrefix(rel, "cmd/conductor/mcp") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		// Walk with the enclosing function tracked, so an exception can be
		// granted per function rather than per file.
		var fn string
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				fn = x.Name.Name
			case *ast.FuncLit:
				// keep the enclosing named function
			case *ast.SelectorExpr:
				if !targetFields[x.Sel.Name] {
					return true
				}
				inner, ok := x.X.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				// `<expr>.Target.<field>` — the dispatch target — or
				// `<expr>.Source.Repo`, the provenance spelling.
				if inner.Sel.Name != "Target" && !(inner.Sel.Name == "Source" && x.Sel.Name == "Repo") {
					return true
				}
				pos := fset.Position(x.Pos())
				if allowed(rel, fn) {
					return true
				}
				k := fmt.Sprintf("%s:%d", rel, pos.Line)
				if seen[k] {
					return true
				}
				seen[k] = true
				flagged = append(flagged, read{file: rel, fn: fn, line: pos.Line})
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(flagged) == 0 {
		return
	}
	sort.Slice(flagged, func(i, j int) bool {
		if flagged[i].file != flagged[j].file {
			return flagged[i].file < flagged[j].file
		}
		return flagged[i].line < flagged[j].line
	})
	var b strings.Builder
	for _, r := range flagged {
		fmt.Fprintf(&b, "\n  %s:%d  in %s()", r.file, r.line, r.fn)
	}
	t.Errorf(`%d unaudited read(s) of the dispatch target.

Each one decides something with a repo/number the event's SENDER may have
chosen (core.Trigger.TargetTrusted). Six review rounds each found one of
these; this test exists so the seventh doesn't have to.

Do ONE of:
  - route it through the trusted accessor — t.OwnRepo(), t.Key(),
    Source.OwnRepo(), memory.NewAgentCaller — if it feeds an authorization,
    an identity, a session/affinity key, a dedup or engagement key, or a
    memory scope; or
  - add it to rawTargetExceptions in this file WITH A REASON, if it only
    logs, displays, notifies, checks out, or fills template data. The reason
    answers one question: if the sender chose this repo, what goes wrong
    here?
%s`, len(flagged), b.String())
}

func allowed(file, fn string) bool {
	for _, e := range rawTargetExceptions[file] {
		if e.fn == fn {
			return true
		}
	}
	return false
}

// Every exception carries a reason, so the list cannot rot into a silencer.
func TestEveryRawTargetExceptionHasAReason(t *testing.T) {
	for file, es := range rawTargetExceptions {
		for _, e := range es {
			if strings.TrimSpace(e.reason) == "" {
				t.Errorf("%s: %s() is excepted with no reason", file, e.fn)
			}
			if strings.TrimSpace(e.fn) == "" {
				t.Errorf("%s: an exception names no function", file)
			}
		}
	}
}
