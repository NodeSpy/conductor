package memory

import (
	"fmt"
	"strings"
	"testing"
)

// REGRESSION: the harvest output contract and the memory IPC tool called
// Remember with NO barrier — a tracked secret in an agent's ```remember
// block (or a live agent's remember tool call) persisted verbatim into
// durable shared memory, unlike the verb and code-binding write paths. Both
// now pass the write guard.
func TestWriteGuardBlocksHarvestAndIPCRemember(t *testing.T) {
	const secret = "mem-s3cr3t-XYZZY"
	m := NewManager(NewMemBackend())
	m.SetWriteGuard(func(text string) error {
		if strings.Contains(text, secret) {
			return fmt.Errorf("memory: refusing to persist tracked secret material")
		}
		return nil
	})

	// Harvest path: a remember block carrying the secret persists NOTHING
	// (all or nothing — even the innocent note stays out).
	out := "done.\n```remember\n- plain note\n- token is " + secret + "\n```"
	entries, err := m.HarvestOutput(out, Source{Agent: "a"})
	if err == nil || !strings.Contains(err.Error(), "refusing to persist") {
		t.Fatalf("harvest of a secret must refuse: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("nothing may persist from a refused harvest: %v", entries)
	}
	if all, _ := m.List(); len(all) != 0 {
		t.Fatalf("memory must stay empty: %v", all)
	}

	// IPC path: the remember op is refused and audited as blocked.
	var audits []map[string]any
	resp := handleIPC(m, IPCRequest{Op: "remember", Text: "key=" + secret, Source: Source{Agent: "a"}},
		func(e map[string]any) { audits = append(audits, e) }, nil)
	if resp.OK || !strings.Contains(resp.Error, "refusing to persist") {
		t.Fatalf("IPC remember of a secret must refuse: %+v", resp)
	}
	var blocked bool
	for _, e := range audits {
		if e["event"] == "memory_remember" && e["outcome"] == "blocked" {
			blocked = true
		}
	}
	if !blocked {
		t.Fatalf("the refusal must be audited: %v", audits)
	}
	if all, _ := m.List(); len(all) != 0 {
		t.Fatalf("memory must stay empty after IPC refusal: %v", all)
	}

	// Clean notes still persist through both paths.
	if _, err := m.HarvestOutput("```remember\n- a plain fact\n```", Source{}); err != nil {
		t.Fatalf("clean harvest must pass: %v", err)
	}
	if resp := handleIPC(m, IPCRequest{Op: "remember", Text: "another fact", Source: Source{}}, nil, nil); !resp.OK {
		t.Fatalf("clean IPC remember must pass: %+v", resp)
	}
	if all, _ := m.List(); len(all) != 2 {
		t.Fatalf("clean notes must persist: %v", all)
	}
}
