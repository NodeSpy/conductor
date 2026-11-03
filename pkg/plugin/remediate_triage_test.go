package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func triageSemantics() *EventSemantics {
	return &EventSemantics{
		Remediate: &RemediateSemantics{Option: "flaky", Run: "run_id",
			Status: RemediateCheck{Verb: "get_run", DoneWhen: "status == 'completed'"}, Action: RemediateVerb{Verb: "rerun"}},
		RemediateTriage: &RemediateTriage{SkipWhen: "stale || conclusion == 'cancelled'",
			Infra: &RemediateInfra{When: "runner_lost", Budget: 2}},
	}
}

func TestRemediateVerdict(t *testing.T) {
	s := triageSemantics()
	for _, c := range []struct {
		name string
		out  map[string]any
		want string
	}{
		{"running", map[string]any{"status": "in_progress", "stale": true}, RemediationWait},
		{"stale", map[string]any{"status": "completed", "conclusion": "failure", "stale": true}, RemediationSkip},
		{"cancelled", map[string]any{"status": "completed", "conclusion": "cancelled"}, RemediationSkip},
		// Skip wins: a superseded run is not re-run even if its runner died.
		{"stale and runner lost", map[string]any{"status": "completed", "conclusion": "failure", "stale": true, "runner_lost": true}, RemediationSkip},
		{"runner lost", map[string]any{"status": "completed", "conclusion": "failure", "runner_lost": true}, RemediationInfra},
		{"failure", map[string]any{"status": "completed", "conclusion": "failure", "stale": false, "runner_lost": false}, RemediationRemedy},
		// A status output without the triage's facts is an ordinary failure.
		{"no facts", map[string]any{"status": "completed"}, RemediationRemedy},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got, err := s.RemediateVerdict(c.out); got != c.want || err != nil {
				t.Fatalf("verdict = %q, %v; want %q", got, err, c.want)
			}
		})
	}

	// No triage declared: every finished run is remedied, as before.
	s.RemediateTriage = nil
	if got, _ := s.RemediateVerdict(map[string]any{"status": "completed", "conclusion": "cancelled", "stale": true}); got != RemediationRemedy {
		t.Fatalf("without remediate_triage: verdict = %q, want remedy", got)
	}
}

func TestValidateSemanticsRemediateTriage(t *testing.T) {
	decl := func(s *EventSemantics) Decl {
		return Decl{Verbs: []Verb{{Name: "get_run"}, {Name: "rerun"}}, Events: []Event{{Name: "fail", Semantics: s}}}
	}
	if p := ValidateSemantics(decl(triageSemantics())); len(p) != 0 {
		t.Fatalf("valid decl reported %q", p)
	}
	for want, mut := range map[string]func(*EventSemantics){
		"needs a remediate":                    func(s *EventSemantics) { s.Remediate = nil },
		"declares neither skip_when nor infra": func(s *EventSemantics) { s.RemediateTriage = &RemediateTriage{} },
		"when is required":                     func(s *EventSemantics) { s.RemediateTriage.Infra.When = "" },
		"budget must not be negative":          func(s *EventSemantics) { s.RemediateTriage.Infra.Budget = -1 },
	} {
		s := triageSemantics()
		mut(s)
		if p := strings.Join(ValidateSemantics(decl(s)), "\n"); !strings.Contains(p, want) {
			t.Fatalf("problems = %q, want one mentioning %q", p, want)
		}
	}
}

// A plugin lists remediate_triage optional so it still loads on a host that
// predates it: describe drops it for that host, which keeps remediate.
func TestRemediateTriageIsStrippedForAnOlderHost(t *testing.T) {
	h := ConnectorFunc(func() Decl {
		s := triageSemantics()
		s.Optional = []string{"remediate_triage"}
		return Decl{Type: "t", Events: []Event{{Name: "fail", Semantics: s}}}
	}, nil)
	resp := roundTrip(t, h, `{"jsonrpc":"2.0","id":1,"method":"plugin.describe","params":{"host":{"semantics":["event.remediate"]}}}`)
	if p := CheckSemantics(resp["1"].Result); len(p) != 0 {
		t.Fatalf("an older host refuses the plugin: %q", p)
	}
	var d Decl
	if err := json.Unmarshal(resp["1"].Result, &d); err != nil {
		t.Fatal(err)
	}
	if s := d.Events[0].Semantics; s.Remediate == nil || s.RemediateTriage != nil {
		t.Fatalf("semantics after strip = %+v, want remediate kept and remediate_triage dropped", s)
	}
}
