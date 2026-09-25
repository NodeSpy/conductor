package flow

import (
	"strings"
	"testing"
)

func TestValidateDecideSteps(t *testing.T) {
	base := `
stores:
  log: { type: sqlite, path: /tmp/decide-validate.db }
  cache: { type: boltdb, path: /tmp/decide-validate.bolt }
models:
  light: { any: ["claude-sonnet-*"] }
workflows:
  w:
    steps:
`
	verify := `      - id: verify
        model: light
        decide:
          state: "{{.meta.title}}"
          questions:
            refuted: { type: noul }
`
	cases := []struct{ name, extra, wantErr string }{
		{"answers are addressable downstream by question name", verify +
			"      - { id: g, if: \"verify.refuted.noul >= 0.8\", log: \"by {{.verify._by}}\" }\n", ""},
		{"an unknown answer is a load error", verify +
			"      - { id: g, if: \"{{.verify.risk.choice}} == 'high'\", log: x }\n", `no output "risk"`},
		{"escalate.when sees only the step's answers", strings.Replace(verify, "            refuted: { type: noul }\n",
			"            refuted: { type: noul }\n          escalate: { when: \"meta.title != ''\" }\n", 1),
			"escalate.when"},
		{"escalate.when over a question passes", strings.Replace(verify, "            refuted: { type: noul }\n",
			"            refuted: { type: noul }\n          escalate: { when: \"refuted.noul > 0.5\" }\n", 1), ""},
		{"a malformed state template is a load error", strings.Replace(verify, "{{.meta.title}}", "{{.meta.title", 1), "decide.state"},
		{"escalate.when may call functions and compare to literals", strings.Replace(verify, "            refuted: { type: noul }\n",
			"            refuted: { type: noul }\n          escalate: { when: \"exists(refuted) && refuted.noul > 0.5 && refuted.noul != 'x' && true\" }\n", 1), ""},
		{"escalate.when templated paths are checked too", strings.Replace(verify, "            refuted: { type: noul }\n",
			"            refuted: { type: noul }\n          escalate: { when: \"{{.pr.draft}}\" }\n", 1), "not one of this step's questions"},
		{"observe needs a SQL store", strings.Replace(verify, "            refuted: { type: noul }\n",
			"            refuted: { type: noul }\n          observe: cache\n", 1), "SQL store"},
		{"observe on a SQL store passes", strings.Replace(verify, "            refuted: { type: noul }\n",
			"            refuted: { type: noul }\n          observe: log\n", 1), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := base + "      - { id: meta, set: { title: t } }\n" + tc.extra
			cfg := loadConfig(t, body)
			err := Validate(cfg, buildRegistry(t, cfg))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want an error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
