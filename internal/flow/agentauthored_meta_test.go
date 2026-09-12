package flow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/skill"
)

// META-TEST A. `skill:` is a capability grant — it mints the broker token
// naming the verbs an agent may call back through. An agent-authored step that
// carried one would be widening its own authority with output it wrote itself.
//
// Three review rounds established that fixing the reported path leaves a
// sibling open, so this asserts the property over EVERY agent-authored entry
// point rather than over one function: whatever route the plan takes, the step
// that reaches dispatch carries no grant.
//
// It catches: a new entry path that builds an agent-authored dispatch without
// going through the strip, and a regression in either plan guard.
func TestNoAgentAuthoredPathGrantsSkill(t *testing.T) {
	grant := &config.SkillPolicy{Verbs: []string{"gh.submit_review"}}

	// Every way a Skill can end up on an agent-authored step.
	for _, tc := range []struct {
		name string
		step config.Step // as it exists when the dispatch is constructed
	}{
		{"direct skill on a plan step", config.Step{
			Type: "agent", Prompt: "p", Skill: grant,
		}},
		{"inherited through a team role reference", func() config.Step {
			// roleStep → config.MergeStepInto pulls the referenced CONFIG
			// step's fields wholesale, grant included. This is the shape that
			// reaches dispatch after that merge.
			s := config.Step{Type: "agent", Prompt: "p", Agent: "worker"}
			config.MergeStepInto(&s, config.Step{Skill: grant, Isolation: &config.IsolationConfig{Mode: "user", User: "nobody"}})
			return s
		}()},
		{"inherited from a saved workflow step", config.Step{
			Type: "agent", Prompt: "p", Skill: grant, Isolation: &config.IsolationConfig{Mode: "user", User: "nobody"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			step := tc.step
			if step.Skill == nil {
				t.Fatal("test setup: this case is supposed to start WITH a grant")
			}

			// 1. The strip at dispatch construction (flow.go) removes it.
			sanitizeAgentAuthoredStep(&step)
			if step.Skill != nil {
				t.Errorf("a %s survived into an agent-authored dispatch: grant %v", tc.name, step.Skill.Verbs)
			}
			if step.Isolation != nil {
				t.Errorf("isolation survived into an agent-authored dispatch: %+v — an inherited "+
					"network policy escapes the deny-by-default egress", step.Isolation)
			}

			// 2. Even if a future path skipped the strip, the read point
			//    refuses to mint anything for an agent-authored request.
			b := skill.NewBroker(func(string) (string, bool) { return "", false }, nil)
			prev := skill.Active()
			skill.SetActive(b)
			defer skill.SetActive(prev)

			req := dispatch.Request{Step: tc.step, AgentAuthored: true}
			if env := dispatch.SkillEnv(req, "unix:///tmp/x.sock"); env != nil {
				t.Errorf("SkillEnv minted a session token for an agent-authored dispatch: %v", env)
			}
			if spec := dispatch.BuildToolServer(req, "unix:///tmp/x.sock"); spec != nil && spec.Env != nil {
				if _, ok := spec.Env["CONDUCTOR_SKILL_CLAIM"]; ok {
					t.Error("BuildToolServer minted a skill claim for an agent-authored dispatch")
				}
			}

			// 3. And an operator-authored dispatch still gets its grant —
			//    otherwise the assertions above would pass by breaking skills.
			opReq := dispatch.Request{Step: tc.step, AgentAuthored: false}
			if env := dispatch.SkillEnv(opReq, "unix:///tmp/x.sock"); env == nil {
				t.Error("an OPERATOR-authored dispatch got no skill env — the refusal is too broad")
			}
		})
	}
}

// Both plan guards must reject every operator-owned field at admission. A new
// field added to forbiddenAgentAuthoredField is covered automatically; a field
// that stops being rejected by either guard fails here.
func TestBothPlanGuardsRejectEveryOperatorOwnedField(t *testing.T) {
	cases := map[string]config.Step{
		"gate:":       {Type: "agent", Prompt: "p", Gate: &config.GateSpec{Run: []string{"x"}}},
		"background:": {Type: "agent", Prompt: "p", Background: true},
		"handoff:":    {Type: "agent", Prompt: "p", Handoff: "slack"},
		"skill:":      {Type: "agent", Prompt: "p", Skill: &config.SkillPolicy{Verbs: []string{"gh.submit_review"}}},
	}
	for field, step := range cases {
		t.Run(field, func(t *testing.T) {
			if err := checkAgentAuthoredFields("plan[0]", &step); err == nil {
				t.Fatalf("an agent-authored step setting %s was admitted", field)
			} else if !strings.Contains(err.Error(), field) {
				t.Fatalf("rejection does not name the field %s: %v", field, err)
			}
		})
	}

	// The list is non-empty and every entry produces a reason, so a field can't
	// be added to the switch with an empty message.
	for field, step := range cases {
		f, why := forbiddenAgentAuthoredField(&step)
		if f == "" || why == "" {
			t.Fatalf("%s: forbiddenAgentAuthoredField returned (%q, %q)", field, f, why)
		}
	}
}

// The chokepoint only holds if both guards actually CALL it. A refactor that
// inlined the checks back into one guard would silently reopen the class for
// the other, so assert the call is present in each.
func TestBothPlanGuardsCallTheSharedFieldCheck(t *testing.T) {
	for _, file := range []string{"guard.go", "plan.go"} {
		src, err := os.ReadFile(filepath.Join(".", file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if !strings.Contains(string(src), "checkAgentAuthoredFields(") {
			t.Errorf("%s no longer calls checkAgentAuthoredFields — the agent-authored "+
				"field policy has to be enforced from one list, or the next field added "+
				"to config.Step will be missed on this path", file)
		}
	}
}

// dispatch.Request literals inside internal/flow that set AgentAuthored must be
// preceded by the strip. Rather than pattern-match call order, assert the far
// simpler invariant that makes the strip reachable: exactly one place in this
// package constructs an agent-authored dispatch, and it calls the sanitizer.
// A second construction site is the regression this catches.
func TestOnlyOneSiteBuildsAnAgentAuthoredDispatch(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var sites []string
	for _, pkg := range pkgs {
		for path, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				sel, ok := lit.Type.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Request" {
					return true
				}
				for _, el := range lit.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "AgentAuthored" {
						// A literal `false` can't grant anything.
						if v, ok := kv.Value.(*ast.Ident); ok && v.Name == "false" {
							continue
						}
						sites = append(sites, filepath.Base(path)+":"+
							strconv.Itoa(fset.Position(kv.Pos()).Line))
					}
				}
				return true
			})
		}
	}
	if len(sites) != 1 {
		t.Fatalf("expected exactly ONE agent-authored dispatch construction site (the one that "+
			"calls sanitizeAgentAuthoredStep); found %d: %v — each additional site is a path "+
			"that can carry a skill grant into an agent-authored launch", len(sites), sites)
	}
	src, err := os.ReadFile("flow.go")
	if err != nil {
		t.Fatalf("read flow.go: %v", err)
	}
	if !strings.Contains(string(src), "sanitizeAgentAuthoredStep(&step)") {
		t.Error("the agent-authored dispatch site no longer strips the inherited grant")
	}
}
