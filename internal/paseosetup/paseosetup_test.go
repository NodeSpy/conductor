package paseosetup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const operatorConfig = `{
  "version": 1,
  "daemon": { "listen": "127.0.0.1:7999" },
  "agents": {
    "providers": {
      "claude": { "label": "Claude", "command": ["/home/op/bin/claude-wrap"], "env": { "X": "1" } },
      "codex": { "enabled": true },
      "codex-sandboxed": { "extends": "codex", "label": "mine", "command": ["my-codex"] },
      "gemini": { "extends": "acp", "command": ["npx", "gemini", "--acp"] }
    }
  },
  "features": { "z": true, "a": false }
}
`

func want() (map[string]map[string]any, []string) {
	return map[string]map[string]any{
		"claude-sandboxed": Entry("claude", "Claude (sandboxed)", "/bin/conductor", nil, []string{"/home/op/bin/claude-wrap"}),
		"codex-sandboxed":  Entry("codex", "Codex (sandboxed)", "/bin/conductor", []string{"--config", "/c.yaml"}, []string{"codex"}),
	}, []string{"claude-sandboxed", "codex-sandboxed"}
}

func providers(t *testing.T, doc []byte) map[string]map[string]any {
	t.Helper()
	var c struct {
		Agents struct {
			Providers map[string]map[string]any `json:"providers"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(doc, &c); err != nil {
		t.Fatalf("%v\n%s", err, doc)
	}
	return c.Agents.Providers
}

func TestEntryShape(t *testing.T) {
	e := Entry("claude", "Claude (sandboxed)", "/bin/conductor", []string{"--config", "/c.yaml"}, []string{"claude-wrap", "--x"})
	b, _ := json.Marshal(e)
	if string(b) != `{"command":["/bin/conductor","sandbox","exec","--config","/c.yaml","--tool","claude","--","claude-wrap","--x"],"extends":"claude","label":"Claude (sandboxed)","x-conductor":true}` {
		t.Fatalf("%s", b)
	}
	if EntryName("pi") != "pi-sandboxed" {
		t.Fatal(EntryName("pi"))
	}
}

func TestMergeAddsSkipsAndPreserves(t *testing.T) {
	w, names := want()
	out, rep, err := Merge([]byte(operatorConfig), w, names)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Results["claude-sandboxed"] != Added || rep.Results["codex-sandboxed"] != Skipped || rep.Count(Added) != 1 || rep.Count(Skipped) != 1 || !rep.Changed() {
		t.Fatalf("report: %+v", rep)
	}
	p := providers(t, out)
	if p["codex-sandboxed"]["label"] != "mine" || p["codex-sandboxed"][Marker] != nil {
		t.Fatalf("an operator entry of the same name is never overwritten: %v", p["codex-sandboxed"])
	}
	before := providers(t, []byte(operatorConfig))
	for name, v := range before {
		bv, _ := json.Marshal(v)
		av, _ := json.Marshal(p[name])
		if string(bv) != string(av) {
			t.Errorf("operator provider %s changed: %s -> %s", name, bv, av)
		}
	}
	if p["claude-sandboxed"][Marker] != true || p["claude-sandboxed"]["extends"] != "claude" {
		t.Fatalf("added: %v", p["claude-sandboxed"])
	}
	// Key order of the file is kept (paseo's own layout, the operator's diff).
	s := string(out)
	if !(strings.Index(s, `"version"`) < strings.Index(s, `"daemon"`) && strings.Index(s, `"daemon"`) < strings.Index(s, `"agents"`) &&
		strings.Index(s, `"agents"`) < strings.Index(s, `"features"`) && strings.Index(s, `"z"`) < strings.Index(s, `"a"`) &&
		strings.Index(s, `"gemini"`) < strings.Index(s, `"claude-sandboxed"`)) {
		t.Fatalf("order not kept:\n%s", s)
	}
	if !strings.HasSuffix(s, "}\n") || !strings.Contains(s, "\n  \"version\": 1,") {
		t.Fatalf("2-space JSON with a trailing newline:\n%s", s)
	}
}

func TestMergeIsIdempotentAndUpdatesOnlyItsOwn(t *testing.T) {
	w, names := want()
	delete(w, "codex-sandboxed")
	names = names[:1]
	out1, rep1, err := Merge([]byte(operatorConfig), w, names)
	if err != nil || rep1.Results["claude-sandboxed"] != Added {
		t.Fatal(rep1, err)
	}
	out2, rep2, err := Merge(out1, w, names)
	if err != nil || rep2.Results["claude-sandboxed"] != Unchanged || rep2.Changed() || string(out2) != string(out1) {
		t.Fatalf("second run is a no-op: %+v %v", rep2, err)
	}
	// Drift in conductor's own entry is corrected.
	drift := strings.Replace(string(out1), "Claude (sandboxed)", "Claude (old)", 1)
	out3, rep3, err := Merge([]byte(drift), w, names)
	if err != nil || rep3.Results["claude-sandboxed"] != Updated || providers(t, out3)["claude-sandboxed"]["label"] != "Claude (sandboxed)" {
		t.Fatalf("update: %+v %v", rep3, err)
	}
	// An entry that merely LOOKS like conductor's (marker false) is the operator's.
	fake := strings.Replace(string(out1), `"x-conductor": true`, `"x-conductor": false`, 1)
	_, rep4, _ := Merge([]byte(fake), w, names)
	if rep4.Results["claude-sandboxed"] != Skipped {
		t.Fatalf("marker must be literally true: %+v", rep4)
	}
	fake = strings.Replace(string(out1), `"x-conductor": true`, `"x-conductor": "true"`, 1)
	_, rep5, _ := Merge([]byte(fake), w, names)
	if rep5.Results["claude-sandboxed"] != Skipped {
		t.Fatalf("a string marker is not the marker: %+v", rep5)
	}
}

func TestMergeEmptyAndMissingBlocks(t *testing.T) {
	w, names := want()
	for _, doc := range []string{"", "{}", `{"agents":{}}`, `{"version":1}`} {
		out, rep, err := Merge([]byte(doc), w, names)
		if err != nil || rep.Count(Added) != 2 {
			t.Fatalf("%q: %+v %v", doc, rep, err)
		}
		if len(providers(t, out)) != 2 {
			t.Fatalf("%q -> %s", doc, out)
		}
	}
	for _, doc := range []string{"[]", "{", `{"agents":[]}`, `{"agents":{"providers":3}}`, `{} {}`} {
		if _, _, err := Merge([]byte(doc), w, names); err == nil {
			t.Errorf("%q must be refused, not rewritten", doc)
		}
	}
}

func TestMergeLeavesUnnamedWantAlone(t *testing.T) {
	w, _ := want()
	_, rep, err := Merge([]byte(operatorConfig), w, []string{"claude-sandboxed", "nope"})
	if err != nil || len(rep.Order) != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestWriteFileKeepsMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte("{}"), 0o640)
	if err := WriteFile(p, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	b, _ := os.ReadFile(p)
	if fi.Mode().Perm() != 0o640 || string(b) != `{"a":1}` {
		t.Fatalf("%v %s", fi.Mode(), b)
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Fatal("no temp file left behind")
	}
}

// Nothing to change: the operator's file comes back byte for byte — never
// reformatted — however it was formatted.
func TestMergeUnchangedKeepsTheBytes(t *testing.T) {
	w, names := want()
	out, _, err := Merge([]byte(operatorConfig), w, names[:1])
	if err != nil {
		t.Fatal(err)
	}
	var v any
	json.Unmarshal(out, &v)
	for _, style := range []func(any) []byte{
		func(v any) []byte { b, _ := json.Marshal(v); return b },
		func(v any) []byte { b, _ := json.MarshalIndent(v, "", "    "); return b },
		func(v any) []byte { b, _ := json.MarshalIndent(v, "", "\t"); return append(b, "\n\n"...) },
	} {
		doc := style(v)
		got, rep, err := Merge(doc, w, names[:1])
		if err != nil || rep.Changed() || string(got) != string(doc) {
			t.Fatalf("an unchanged merge rewrote the file (%+v %v):\n%s", rep, err, got)
		}
	}
}
