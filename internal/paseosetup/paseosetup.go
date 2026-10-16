// Package paseosetup writes conductor's provider entries into a paseo
// daemon's config (`conductor paseo setup`): paseo keeps its native
// claude/codex/pi providers — rewind, steer, subsessions — and a
// conductor-owned entry per provider runs the real agent command through
// `conductor sandbox exec`, so every session started with it is sandboxed.
//
//	"claude-sandboxed": { "extends": "claude", "label": "Claude (sandboxed)",
//	  "command": ["/path/conductor", "sandbox", "exec", "--tool", "claude", "--", "claude"],
//	  "x-conductor": true }
//
// The merge is idempotent and touches only conductor's own entries, marked
// "x-conductor": true. A same-named entry WITHOUT the marker is the
// operator's: it is skipped, never overwritten. Every other key of the file
// keeps its value and its place.
package paseosetup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// Marker is the key that makes a provider entry conductor's own.
const Marker = "x-conductor"

// Suffix names a sandboxed entry after the provider it extends.
const Suffix = "-sandboxed"

// EntryName is the sandboxed entry for provider ("claude" → "claude-sandboxed").
func EntryName(provider string) string { return provider + Suffix }

// Providers are the paseo providers conductor writes an entry for, with
// their labels. pi is written only where paseo reports it available.
var Providers = []struct{ Name, Label string }{
	{"claude", "Claude (sandboxed)"},
	{"codex", "Codex (sandboxed)"},
	{"pi", "Pi (sandboxed)"},
}

// Entry builds conductor's provider entry for provider: the real agent
// command (agentCmd) under `<conductor> sandbox exec --tool <provider>`,
// with daemonArgs (--config/--socket) when the daemon is not the default.
func Entry(provider, label, conductor string, daemonArgs, agentCmd []string) map[string]any {
	cmd := []any{conductor, "sandbox", "exec"}
	for _, a := range daemonArgs {
		cmd = append(cmd, a)
	}
	cmd = append(cmd, "--tool", provider, "--")
	for _, a := range agentCmd {
		cmd = append(cmd, a)
	}
	return map[string]any{
		"extends": provider,
		"label":   label,
		"command": cmd,
		Marker:    true,
	}
}

// Outcome of one entry's merge.
type Outcome string

const (
	Added     Outcome = "added"
	Updated   Outcome = "updated"
	Unchanged Outcome = "unchanged"
	Skipped   Outcome = "skipped" // an operator entry of the same name: never touched
)

// Report is what a merge did, per entry name.
type Report struct {
	Results map[string]Outcome
	Order   []string
}

// Count reports how many entries had outcome o.
func (r Report) Count(o Outcome) int {
	n := 0
	for _, v := range r.Results {
		if v == o {
			n++
		}
	}
	return n
}

// Changed reports a merge that changed the config.
func (r Report) Changed() bool { return r.Count(Added)+r.Count(Updated) > 0 }

// Merge merges want (entry name → entry) into a paseo config document and
// returns the new document. An existing entry is replaced only when it
// carries the marker; one without it is the operator's and is skipped.
func Merge(doc []byte, want map[string]map[string]any, names []string) ([]byte, Report, error) {
	rep := Report{Results: map[string]Outcome{}}
	if len(bytes.TrimSpace(doc)) == 0 {
		doc = []byte("{}")
	}
	top, err := parseObject(doc)
	if err != nil {
		return nil, rep, fmt.Errorf("paseo config: %w", err)
	}
	agentsRaw, _ := top.get("agents")
	if agentsRaw == nil {
		agentsRaw = []byte("{}")
	}
	agents, err := parseObject(agentsRaw)
	if err != nil {
		return nil, rep, fmt.Errorf("paseo config: agents: %w", err)
	}
	provRaw, _ := agents.get("providers")
	if provRaw == nil {
		provRaw = []byte("{}")
	}
	provs, err := parseObject(provRaw)
	if err != nil {
		return nil, rep, fmt.Errorf("paseo config: agents.providers: %w", err)
	}
	for _, name := range names {
		w, ok := want[name]
		if !ok {
			continue
		}
		rep.Order = append(rep.Order, name)
		wb, err := json.Marshal(w)
		if err != nil {
			return nil, rep, err
		}
		cur, exists := provs.get(name)
		if !exists {
			provs.set(name, wb)
			rep.Results[name] = Added
			continue
		}
		var curMap map[string]any
		if json.Unmarshal(cur, &curMap) != nil || curMap[Marker] != true {
			rep.Results[name] = Skipped
			continue
		}
		var wantMap map[string]any
		_ = json.Unmarshal(wb, &wantMap)
		if reflect.DeepEqual(curMap, wantMap) {
			rep.Results[name] = Unchanged
			continue
		}
		provs.set(name, wb)
		rep.Results[name] = Updated
	}
	if !rep.Changed() {
		return doc, rep, nil
	}
	agents.set("providers", provs.marshal())
	top.set("agents", agents.marshal())
	var out bytes.Buffer
	if err := json.Indent(&out, top.marshal(), "", "  "); err != nil {
		return nil, rep, err
	}
	out.WriteByte('\n')
	return out.Bytes(), rep, nil
}

// WriteFile replaces path with b atomically, keeping its mode.
func WriteFile(path string, b []byte) error {
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".config.json.conductor-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// object is a JSON object with its key order kept.
type object struct {
	keys []string
	vals map[string]json.RawMessage
}

func parseObject(b []byte) (*object, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("not a JSON object")
	}
	o := &object{vals: map[string]json.RawMessage{}}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k := kt.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if _, dup := o.vals[k]; !dup {
			o.keys = append(o.keys, k)
		}
		o.vals[k] = v
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after the object")
	}
	return o, nil
}

func (o *object) get(k string) (json.RawMessage, bool) {
	v, ok := o.vals[k]
	return v, ok
}

func (o *object) set(k string, v json.RawMessage) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *object) marshal() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(o.vals[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}
