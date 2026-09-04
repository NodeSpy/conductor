package flow

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
)

// Two heterogeneous sources: the fake type's ping event accepts the filter
// key `only`; a rest connector's polled event declares no filter keys at all.
const onListBase = `
connectors:
  svc: { type: fake }
  api:
    type: rest
    base_url: http://api.invalid
    verbs:
      noop: { method: GET, path: / }
    events:
      new_thing:
        request: { path: /things }
        list: "{{.response.body.Things}}"
        id: "{{.item.ID}}"
`

// TestOnListPerSourceFilterValidation: each per-source filters: block is
// checked against ITS source's schema only — a key legal on one source
// doesn't have to be legal on the others.
func TestOnListPerSourceFilterValidation(t *testing.T) {
	cfg := loadConfig(t, onListBase+`
triggers:
  - name: fan-in
    on:
      - svc.ping: { filters: { only: hello } }
      - api.new_thing
    steps: [ { uses: svc.post, options: { text: t } } ]
`)
	reg := buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err != nil {
		t.Fatalf("per-source filter on its own source must pass: %v", err)
	}

	// The same key on the source that does NOT declare it fails, naming it.
	cfg = loadConfig(t, onListBase+`
triggers:
  - name: fan-in
    on:
      - svc.ping
      - api.new_thing: { filters: { only: hello } }
    steps: [ { uses: svc.post, options: { text: t } } ]
`)
	reg = buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err == nil || !strings.Contains(err.Error(), `"only"`) {
		t.Fatalf("filter key unknown to its source must fail: %v", err)
	}
}

// TestOnListSharedBaseIntersection: a top-level filters: base applies to
// every listed source, so a base key one source doesn't accept is a load
// error (the intersection rule).
func TestOnListSharedBaseIntersection(t *testing.T) {
	cfg := loadConfig(t, onListBase+`
triggers:
  - name: fan-in
    on: [svc.ping, api.new_thing]
    filters: { only: hello }
    steps: [ { uses: svc.post, options: { text: t } } ]
`)
	reg := buildRegistry(t, cfg)
	err := Validate(cfg, reg)
	if err == nil || !strings.Contains(err.Error(), `"only"`) || !strings.Contains(err.Error(), "api.new_thing") {
		t.Fatalf("base key outside the intersection must fail naming the source: %v", err)
	}

	// manual accepts no filters, so a base over [x, manual] fails too.
	cfg = loadConfig(t, onListBase+`
triggers:
  - name: fan-in
    on: [svc.ping, manual]
    filters: { only: hello }
    steps: [ { uses: svc.post, options: { text: t } } ]
`)
	reg = buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err == nil || !strings.Contains(err.Error(), "manual source accepts no filters") {
		t.Fatalf("base filters over manual must fail: %v", err)
	}
}

// TestOnListFilterPrecedence: at runtime, a per-source filters: key overrides
// the shared base for that source — the expanded triggers carry the merged
// blocks FilterMatch evaluates.
func TestOnListFilterPrecedence(t *testing.T) {
	cfg := loadConfig(t, onListBase+`
triggers:
  - name: fan-in
    on:
      - svc.ping
      - svc2.ping: { filters: { only: beta } }
    filters: { only: alpha }
    steps: [ { uses: svc.post, options: { text: t } } ]
`)
	// Second fake instance so both sources have the `only` schema.
	cfg.ConnectorsMap["svc2"] = cfg.ConnectorsMap["svc"]
	reg := buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Cfg: cfg, Conns: reg}

	// Base source: base filter (only: alpha) applies.
	ev := core.Trigger{Context: map[string]any{"msg": "alpha says hi"}}
	if ok, _ := r.FilterMatch(ev, cfg.Triggers[0]); !ok {
		t.Fatal("base filter must match alpha on the bare source")
	}
	if ok, _ := r.FilterMatch(core.Trigger{Context: map[string]any{"msg": "beta"}}, cfg.Triggers[0]); ok {
		t.Fatal("bare source must still use the base filter")
	}
	// Overridden source: only: beta replaced the base.
	if ok, _ := r.FilterMatch(core.Trigger{Context: map[string]any{"msg": "beta says hi"}}, cfg.Triggers[1]); !ok {
		t.Fatal("per-source filter must override the base")
	}
	if ok, _ := r.FilterMatch(ev, cfg.Triggers[1]); ok {
		t.Fatal("base value must not leak into the overridden source")
	}
}

// TestOnListUnknownSource: an unknown connector in the list fails naming the
// declared ones.
func TestOnListUnknownSource(t *testing.T) {
	cfg := loadConfig(t, onListBase+`
triggers:
  - name: fan-in
    on: [svc.ping, ghost.event]
    steps: [ { uses: svc.post, options: { text: t } } ]
`)
	reg := buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err == nil || !strings.Contains(err.Error(), `unknown connector "ghost"`) {
		t.Fatalf("unknown source: %v", err)
	}
}

// TestManualTriggerValidation: manual steps validate in an open scope with
// `inputs` addressable; filters/options on manual are rejected.
func TestManualTriggerValidation(t *testing.T) {
	cfg := loadConfig(t, onListBase+`
workflows:
  clone:
    inputs:
      contact_id: { type: string, required: true }
    steps: [ { uses: svc.post, options: { text: "{{.inputs.contact_id}}" } } ]
triggers:
  - name: clone-invoice
    on: [manual, api.new_thing]
    steps:
      - { workflow: clone, with: { contact_id: '{{ .item.ID | default .inputs.contact_id }}' } }
`)
	reg := buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err != nil {
		t.Fatalf("manual + heterogeneous refs (defensive default) must pass: %v", err)
	}

	cfg = loadConfig(t, onListBase+`
triggers:
  - name: adhoc
    on: manual
    options: { nope: 1 }
    steps: [ { uses: svc.post, options: { text: t } } ]
`)
	reg = buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err == nil || !strings.Contains(err.Error(), "manual source accepts no options") {
		t.Fatalf("manual options must be rejected: %v", err)
	}

	// Manual triggers still validate group keys and hooks: valid refs pass,
	// an unknown hook verb fails.
	cfg = loadConfig(t, onListBase+`
triggers:
  - name: batchy
    on: manual
    group: { key: "{{.inputs.batch}}" }
    hooks:
      - { at: start, uses: svc.post, options: { text: "go" } }
      - { at: done, uses: svc.post, options: { text: "ok" } }
      - { at: fail, uses: svc.post, options: { text: "{{.error}}" } }
    steps: [ { uses: svc.post, options: { text: t } } ]
`)
	reg = buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err != nil {
		t.Fatalf("manual group + hooks must validate: %v", err)
	}
	cfg = loadConfig(t, onListBase+`
triggers:
  - name: adhoc
    on: manual
    hooks: [ { at: done, uses: svc.zap, options: {} } ]
    steps: [ { uses: svc.post, options: { text: t } } ]
`)
	reg = buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err == nil || !strings.Contains(err.Error(), `no verb "zap"`) {
		t.Fatalf("manual hook verb must be checked: %v", err)
	}
}
