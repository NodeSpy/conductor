// Package systemone is conductor's copy of the `system_one/v1` decision
// contract — TypeSafe's published System One API (POST /v1/systemone), taken
// as-is so a decide: step speaks the same wire shape whether a native
// decision runtime (Jev) or an agent runtime answers it.
//
// The contract, in one breath: a request carries an unstructured `state` and
// named `questions`, each one of three types —
//
//   - noul:   a yes/no question; the answer is `noul`, the probability of yes.
//   - choice: pick one of a set of labels; the answer is `choice` (the most
//     probable label), `confidence`, and `probabilities` per label.
//   - score:  rate against an ordered rubric; the answer is `score` (the
//     probability-weighted average level), `confidence`, `legend`, and
//     `probabilities` per level.
//
// This package owns three things and nothing else: the question and answer
// shapes with their validation (so a runtime's reply is never trusted
// unchecked), and the ADAPTER — the rendering of a request into a prompt plus
// output schema any agent runtime can answer, and the conversion of that reply
// back into v1 answers. The adapter is a Go port of the parts of TypeSafe's
// MIT-licensed system-one-adapter that decide answer quality (schema and field
// descriptions, probability normalization, confidence metrics); its
// provider-API calls are deliberately not ported — an agent runtime's own
// session replaces them, so no model-provider key ever enters conductor. See
// adapter.go for the license notice.
//
// Nothing here imports the rest of conductor: it is a leaf, so the config
// loader, the flow runner, and the runtime-plugin wiring can all share it.
package systemone

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProtocolV1 is the protocol identifier a decide: step defaults to and a
// decision runtime declares it speaks.
const ProtocolV1 = "system_one/v1"

// Question types.
const (
	TypeNoul   = "noul"
	TypeChoice = "choice"
	TypeScore  = "score"
)

// minCriteria is the fewest outcomes a choice or score question may offer —
// one outcome is not a decision. Mirrors the reference adapter.
const minCriteria = 2

// probabilityTolerance is how far a distribution's sum may drift from 1
// before it is rescaled. Mirrors the reference adapter.
const probabilityTolerance = 1e-6

// Criterion is one labelled outcome of a choice question: the label the
// answer names and a description of what earns it (text, or any JSON value).
type Criterion struct {
	Label string
	Desc  any
}

// Question is one typed question. Exactly the fields its Type uses are set:
//
//   - noul:   Instructions, and optionally True/False (HasNoulCriteria).
//   - choice: Instructions (optional) and Choices, in declaration order.
//   - score:  Instructions (optional) and Levels, lowest first (level 0).
//
// Order is preserved end to end — it is the order labels are listed to a
// model and the order a probability tie breaks in — which is why choice
// criteria are a slice rather than a map.
type Question struct {
	Type         string
	Instructions any

	Choices []Criterion // choice
	Levels  []any       // score

	HasNoulCriteria bool // noul: a criteria block was written
	True, False     any  // noul: what counts as yes / no
}

// NamedQuestion is a question with its name — the key its answer appears
// under.
type NamedQuestion struct {
	Name string
	Question
}

// Questions is an ordered set of named questions. YAML decodes it from a
// mapping and keeps the mapping's order.
type Questions []NamedQuestion

// Names lists the question names in order.
func (qs Questions) Names() []string {
	out := make([]string, len(qs))
	for i, q := range qs {
		out[i] = q.Name
	}
	return out
}

// Get finds a question by name.
func (qs Questions) Get(name string) (Question, bool) {
	for _, q := range qs {
		if q.Name == name {
			return q.Question, true
		}
	}
	return Question{}, false
}

// UnmarshalYAML decodes a `questions:` mapping, preserving order.
func (qs *Questions) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("questions: must be a mapping of question name → { type, instructions, criteria }")
	}
	out := make(Questions, 0, len(n.Content)/2)
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		name := n.Content[i].Value
		if seen[name] {
			return fmt.Errorf("questions: duplicate question %q", name)
		}
		seen[name] = true
		var q Question
		if err := q.decode(n.Content[i+1]); err != nil {
			return fmt.Errorf("questions.%s: %w", name, err)
		}
		out = append(out, NamedQuestion{Name: name, Question: q})
	}
	*qs = out
	return nil
}

// MarshalYAML emits the mapping form in order, so a config that round-trips
// (pack lowering, migration) keeps its questions intact.
func (qs Questions) MarshalYAML() (any, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, q := range qs {
		v := &yaml.Node{}
		if err := v.Encode(q.Question.wire()); err != nil {
			return nil, err
		}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: q.Name}, v)
	}
	return m, nil
}

// decode reads one question node.
func (q *Question) decode(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("a question is a mapping: { type: noul|choice|score, instructions, criteria }")
	}
	var criteria *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i].Value, n.Content[i+1]
		switch key {
		case "type":
			q.Type = strings.TrimSpace(val.Value)
		case "instructions":
			var v any
			if err := val.Decode(&v); err != nil {
				return fmt.Errorf("instructions: %w", err)
			}
			q.Instructions = v
		case "criteria":
			criteria = val
		default:
			return fmt.Errorf("unknown field %q (a question has type, instructions, criteria)", key)
		}
	}
	if criteria == nil || (criteria.Kind == yaml.ScalarNode && criteria.Tag == "!!null") {
		return nil
	}
	switch q.Type {
	case TypeChoice:
		if criteria.Kind != yaml.MappingNode {
			return fmt.Errorf("criteria: a choice question's criteria map each label to what earns it")
		}
		for i := 0; i+1 < len(criteria.Content); i += 2 {
			var d any
			if err := criteria.Content[i+1].Decode(&d); err != nil {
				return fmt.Errorf("criteria.%s: %w", criteria.Content[i].Value, err)
			}
			q.Choices = append(q.Choices, Criterion{Label: criteria.Content[i].Value, Desc: d})
		}
	case TypeScore:
		if criteria.Kind != yaml.SequenceNode {
			return fmt.Errorf("criteria: a score question's criteria are an ordered list of levels, lowest first")
		}
		for i, item := range criteria.Content {
			var d any
			if err := item.Decode(&d); err != nil {
				return fmt.Errorf("criteria[%d]: %w", i, err)
			}
			q.Levels = append(q.Levels, d)
		}
	case TypeNoul:
		if criteria.Kind != yaml.MappingNode {
			return fmt.Errorf("criteria: a noul question's criteria are { true: …, false: … }")
		}
		q.HasNoulCriteria = true
		for i := 0; i+1 < len(criteria.Content); i += 2 {
			var d any
			if err := criteria.Content[i+1].Decode(&d); err != nil {
				return fmt.Errorf("criteria.%s: %w", criteria.Content[i].Value, err)
			}
			switch criteria.Content[i].Value {
			case "true":
				q.True = d
			case "false":
				q.False = d
			default:
				return fmt.Errorf("criteria: a noul question's criteria keys are true and false, not %q", criteria.Content[i].Value)
			}
		}
	default:
		// Type is checked in Validate; keep the raw shape out of the way.
		return nil
	}
	return nil
}

// Validate checks one question against the contract.
func (q Question) Validate() error {
	switch q.Type {
	case TypeNoul:
		if len(q.Choices) > 0 || len(q.Levels) > 0 {
			return fmt.Errorf("a noul question takes criteria { true, false }, not labels or levels")
		}
	case TypeChoice:
		if len(q.Choices) < minCriteria {
			return fmt.Errorf("a choice question needs at least %d labelled criteria (got %d)", minCriteria, len(q.Choices))
		}
		seen := map[string]bool{}
		for _, c := range q.Choices {
			if strings.TrimSpace(c.Label) == "" {
				return fmt.Errorf("a choice label is empty")
			}
			if seen[c.Label] {
				return fmt.Errorf("duplicate choice label %q", c.Label)
			}
			seen[c.Label] = true
		}
	case TypeScore:
		if len(q.Levels) < minCriteria {
			return fmt.Errorf("a score question needs at least %d rubric levels (got %d)", minCriteria, len(q.Levels))
		}
	case "":
		return fmt.Errorf("type is required (noul, choice, or score)")
	default:
		return fmt.Errorf("unknown type %q (noul, choice, or score)", q.Type)
	}
	return nil
}

// Validate checks a question set: at least one question, names usable as
// output keys, and every question well-formed.
func (qs Questions) Validate() error {
	if len(qs) == 0 {
		return fmt.Errorf("at least one question is required")
	}
	for _, q := range qs {
		if err := ValidName(q.Name); err != nil {
			return err
		}
		if err := q.Question.Validate(); err != nil {
			return fmt.Errorf("question %q: %w", q.Name, err)
		}
	}
	return nil
}

// ValidName reports whether a question name is usable as an output key. A
// name must be an identifier (it is referenced as {{.<step>.<name>.noul}} and
// in escalate.when), and a leading underscore is reserved for conductor's own
// output fields (`_by`, `_escalated_from`).
func ValidName(name string) error {
	if name == "" {
		return fmt.Errorf("a question name is empty")
	}
	if strings.HasPrefix(name, "_") {
		return fmt.Errorf("question name %q: a leading underscore is reserved for conductor's own output fields", name)
	}
	for i, r := range name {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("question name %q: use letters, digits and underscores (it is referenced as a template path)", name)
		}
	}
	return nil
}

// Labels returns a question's answer keys in order: the choice labels, or
// the score levels as "0".."n-1". Nil for noul.
func (q Question) Labels() []string {
	switch q.Type {
	case TypeChoice:
		out := make([]string, len(q.Choices))
		for i, c := range q.Choices {
			out[i] = c.Label
		}
		return out
	case TypeScore:
		out := make([]string, len(q.Levels))
		for i := range q.Levels {
			out[i] = strconv.Itoa(i)
		}
		return out
	}
	return nil
}

// wire is the question's JSON/YAML wire form: { type, instructions?,
// criteria? }. A choice's criteria become an object; its label order is not
// carried on this form (JSON objects are unordered), which only matters for
// a probability tie — resolved by the answering runtime.
func (q Question) wire() map[string]any {
	out := map[string]any{"type": q.Type}
	if q.Instructions != nil {
		out["instructions"] = q.Instructions
	}
	switch q.Type {
	case TypeChoice:
		c := make(map[string]any, len(q.Choices))
		for _, ch := range q.Choices {
			c[ch.Label] = ch.Desc
		}
		out["criteria"] = c
	case TypeScore:
		out["criteria"] = q.Levels
	case TypeNoul:
		if q.HasNoulCriteria {
			out["criteria"] = map[string]any{"true": q.True, "false": q.False}
		}
	}
	return out
}

// Wire is the `questions` object of a v1 request.
func (qs Questions) Wire() map[string]any {
	out := make(map[string]any, len(qs))
	for _, q := range qs {
		out[q.Name] = q.Question.wire()
	}
	return out
}

// Request is one v1 request.
type Request struct {
	Model     string
	State     any
	Questions Questions
}

// Wire is the request body: { model, state, questions }. Model is omitted
// when empty (the runtime's own default model).
func (r Request) Wire() map[string]any {
	out := map[string]any{"state": r.State, "questions": r.Questions.Wire()}
	if r.Model != "" {
		out["model"] = r.Model
	}
	return out
}

// ---------------------------------------------------------------------------
// Answers
// ---------------------------------------------------------------------------

// ValidateAnswers checks a reply's `answers` object against the questions
// and returns a clean copy: exactly one answer per question, each of the
// question's type, every number a finite probability in range. Answers to
// questions that were not asked are dropped rather than passed downstream.
//
// A runtime's reply is never trusted unchecked — a decision-runtime plugin is
// third-party code, and an agent's JSON is a model's output — so every
// answer, from every backend, goes through here.
func ValidateAnswers(qs Questions, raw any) (map[string]any, error) {
	answers, ok := asMap(raw)
	if !ok {
		return nil, fmt.Errorf("answers must be an object keyed by question name")
	}
	out := make(map[string]any, len(qs))
	for _, q := range qs {
		a, present := answers[q.Name]
		if !present {
			return nil, fmt.Errorf("no answer for question %q", q.Name)
		}
		clean, err := validateAnswer(q.Question, a)
		if err != nil {
			return nil, fmt.Errorf("answer %q: %w", q.Name, err)
		}
		out[q.Name] = clean
	}
	return out, nil
}

func validateAnswer(q Question, raw any) (map[string]any, error) {
	a, ok := asMap(raw)
	if !ok {
		return nil, fmt.Errorf("must be an object")
	}
	if t, _ := a["type"].(string); t != q.Type {
		return nil, fmt.Errorf("type is %q, the question is %q", a["type"], q.Type)
	}
	switch q.Type {
	case TypeNoul:
		p, err := probability(a["noul"], "noul")
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": TypeNoul, "noul": p}, nil
	case TypeChoice:
		choice, _ := a["choice"].(string)
		if !contains(q.Labels(), choice) {
			return nil, fmt.Errorf("choice %q is not one of %s", choice, strings.Join(q.Labels(), ", "))
		}
		conf, err := probability(a["confidence"], "confidence")
		if err != nil {
			return nil, err
		}
		probs, err := distribution(a["probabilities"], q.Labels())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": TypeChoice, "choice": choice, "confidence": conf, "probabilities": probs}, nil
	case TypeScore:
		score, err := number(a["score"], "score")
		if err != nil {
			return nil, err
		}
		if top := float64(len(q.Levels) - 1); score < 0 || score > top+probabilityTolerance {
			return nil, fmt.Errorf("score %v is outside the rubric (0..%v)", score, top)
		}
		conf, err := probability(a["confidence"], "confidence")
		if err != nil {
			return nil, err
		}
		probs, err := distribution(a["probabilities"], q.Labels())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": TypeScore, "score": score, "confidence": conf,
			"legend": legend(q), "probabilities": probs}, nil
	}
	return nil, fmt.Errorf("unknown question type %q", q.Type)
}

// legend is a score question's rubric keyed "0".."n-1", as the v1 answer
// carries it. Conductor builds it from the question rather than echoing a
// runtime's, so it always matches what was asked.
func legend(q Question) map[string]any {
	out := make(map[string]any, len(q.Levels))
	for i, l := range q.Levels {
		out[strconv.Itoa(i)] = l
	}
	return out
}

// distribution checks a probability map: exactly the expected keys, each a
// probability. The sum is NOT forced to 1 here — a native runtime's
// distribution is reported as it came ("values sum to approximately 1").
func distribution(raw any, labels []string) (map[string]any, error) {
	m, ok := asMap(raw)
	if !ok {
		return nil, fmt.Errorf("probabilities must be an object keyed by %s", strings.Join(labels, ", "))
	}
	out := make(map[string]any, len(labels))
	for _, l := range labels {
		p, err := probability(m[l], "probabilities."+l)
		if err != nil {
			return nil, err
		}
		out[l] = p
	}
	return out, nil
}

func probability(v any, field string) (float64, error) {
	f, err := number(v, field)
	if err != nil {
		return 0, err
	}
	if f < 0 || f > 1 {
		return 0, fmt.Errorf("%s %v is not a probability (0..1)", field, f)
	}
	return f, nil
}

func number(v any, field string) (float64, error) {
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case float32:
		f = float64(n)
	case int:
		f = float64(n)
	case int64:
		f = float64(n)
	case json.Number:
		x, err := n.Float64()
		if err != nil {
			return 0, fmt.Errorf("%s is not a number", field)
		}
		f = x
	case nil:
		return 0, fmt.Errorf("%s is missing", field)
	default:
		return 0, fmt.Errorf("%s must be a number, got %T", field, v)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%s is not a finite number", field)
	}
	return f, nil
}

func asMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[fmt.Sprint(k)] = val
		}
		return out, true
	}
	return nil, false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// DefaultAnswers builds the answers a decide: step falls back to when every
// candidate failed. Each entry may be a full v1 answer, or just its headline
// value — `{ noul: 0 }`, `{ choice: high }`, `{ score: 0 }` — in which case
// the rest is filled in honestly: a default is not a measurement, so
// confidence is 0 and the distribution is uniform. `type` may be omitted
// (it is the question's). Every question must have a default.
func DefaultAnswers(qs Questions, raw map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(qs))
	for name := range raw {
		if _, ok := qs.Get(name); !ok {
			return nil, fmt.Errorf("default %q names no question (questions: %s)", name, strings.Join(qs.Names(), ", "))
		}
	}
	for _, q := range qs {
		v, ok := raw[q.Name]
		if !ok {
			return nil, fmt.Errorf("no default for question %q — a default must answer every question", q.Name)
		}
		m, ok := asMap(v)
		if !ok {
			return nil, fmt.Errorf("default %q must be an object, e.g. { %s }", q.Name, headlineExample(q.Question))
		}
		full := make(map[string]any, len(m)+4)
		for k, x := range m {
			full[k] = x
		}
		if _, ok := full["type"]; !ok {
			full["type"] = q.Type
		}
		if q.Type != TypeNoul {
			labels := q.Labels()
			if _, ok := full["confidence"]; !ok {
				full["confidence"] = 0.0
			}
			if _, ok := full["probabilities"]; !ok {
				u := make(map[string]any, len(labels))
				for _, l := range labels {
					u[l] = 1 / float64(len(labels))
				}
				full["probabilities"] = u
			}
		}
		clean, err := validateAnswer(q.Question, full)
		if err != nil {
			return nil, fmt.Errorf("default %q: %w", q.Name, err)
		}
		out[q.Name] = clean
	}
	return out, nil
}

func headlineExample(q Question) string {
	switch q.Type {
	case TypeChoice:
		if len(q.Choices) > 0 {
			return "choice: " + q.Choices[0].Label
		}
		return "choice: <label>"
	case TypeScore:
		return "score: 0"
	}
	return "noul: 0"
}
