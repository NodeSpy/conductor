package flow

import (
	"strings"
	"testing"
)

// TestValidateRESTOpenVerbs: rest/graphql verbs take user-defined option
// patterns (Open), so arbitrary option keys pass — while references to
// outputs the verb did NOT declare still fail at load time.
func TestValidateRESTOpenVerbs(t *testing.T) {
	base := `
connectors:
  api:
    type: rest
    base_url: http://api.invalid
    verbs:
      list:
        method: GET
        path: /Invoices/{{.options.contact}}
        output: { first_id: "{{ (index .response.body.Invoices 0).InvoiceID }}" }
      create:
        method: POST
        path: /Invoices
        body: "{{ .options.invoice | json }}"
    events:
      new_invoice:
        request: { path: /Invoices }
        list: "{{.response.body.Invoices}}"
        id: "{{.item.InvoiceID}}"
        context: { total: "{{.item.Total}}" }
`
	cases := []struct {
		name, trigger, wantErr string
	}{
		{
			"arbitrary option keys pass on an Open verb",
			"- on: api.new_invoice\n  steps: [{id: s1, uses: api.list, options: {contact: c-1, anything: [1, 2]}}]",
			"",
		},
		{
			"declared output is addressable downstream",
			"- on: api.new_invoice\n  steps:\n    - {id: s1, uses: api.list, options: {contact: c}}\n    - {uses: api.create, options: {invoice: '{{.s1.first_id}}'}}",
			"",
		},
		{
			"undeclared output still fails",
			"- on: api.new_invoice\n  steps:\n    - {id: s1, uses: api.list, options: {contact: c}}\n    - {uses: api.create, options: {invoice: '{{.s1.nope}}'}}",
			`no output "nope"`,
		},
		{
			"unknown rest verb still fails",
			"- on: api.new_invoice\n  steps: [{uses: api.zap, options: {}}]",
			`no verb "zap"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadConfig(t, base+"triggers:\n"+indent(tc.trigger, "  ")+"\n")
			reg := buildRegistry(t, cfg)
			err := Validate(cfg, reg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
