package code

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// REGRESSION: ctx.store/ctx.sql/ctx.memory writes bypassed the plan write
// barrier entirely (it was wired into the `uses: kv.set` verb path only) —
// an agent plan's code step could park secret material the verb would have
// refused. Every data-plane op now consults Spec.DataGuard, passing the
// STORE NAME as the resource (#124); the guard decides which ops it cares
// about.
//
// This used to drive the four in-process engines. It drives the `cli` engine
// instead — a REAL subprocess reaching the data plane over its per-run
// socket — which is the stronger version of the same assertion: the guard
// holds even though the code doing the asking is not in this process and
// holds no store handle of its own.
func TestDataGuardBlocksDataPlaneWrites(t *testing.T) {
	needSh(t)
	const secret = "c0de-s3cr3t-value"
	var vetted []string
	guard := func(kind, op, resource string, args []any) error {
		vetted = append(vetted, kind+"."+op+"@"+resource)
		if strings.Contains(fmt.Sprint(args...), secret) {
			return fmt.Errorf("no_secret_egress: refusing to write secret material into %s.%s", kind, op)
		}
		return nil
	}
	helper := ctxHelperScript(t)
	e := &Executor{}

	// A cli step that tries to park the secret in kv. The client exits 3 on a
	// policy refusal, so the step reports the code rather than the message.
	tempKV(t)
	out, err := e.Exec(context.Background(), Spec{
		Run: "cli", Command: []string{"sh"}, Code: `
"$HELPER" ctx kv s set ns loot "$LEAK" >/dev/null 2>&1
leaked=$?
"$HELPER" ctx kv s set ns note plain >/dev/null || exit 1
got=$("$HELPER" ctx kv s get ns note) || exit 1
printf '{"leaked": %d, "got": %s}' "$leaked" "$got"
`,
		Env:       map[string]string{"HELPER": helper, ctxClientHelperEnv: "1", "LEAK": secret},
		DataGuard: guard,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if toIntT(t, out["leaked"]) != ctxExitRefused {
		t.Errorf("kv.set of a secret must be refused as policy, got exit %v", out["leaked"])
	}
	if out["got"] != "plain" {
		t.Errorf("a clean write/read on the same guard must pass: %#v", out)
	}

	// sql and memory go through the same guard, with their own kinds. Driven
	// through the handler directly — the transport is already proven above and
	// what is under test here is that no kind escapes the barrier.
	tempSQL(t)
	tempMem(t)
	h := CtxHandler{Guard: guard}
	if res := h.Invoke(CtxRequest{Kind: CtxKindSQL, Op: "exec", Resource: "db",
		Args: []any{"INSERT INTO events (body) VALUES (?)", []any{secret}}}); res.OK || !res.Refused {
		t.Errorf("sql.exec of a secret must be refused: %#v", res)
	}
	// A NAMED scope, so this exercises the secret barrier specifically —
	// "global" would be refused one layer earlier by the reserved-bucket rule
	// and the assertion would pass without the guard running at all.
	if res := h.Invoke(CtxRequest{Kind: CtxKindMemory, Op: "remember",
		Args: []any{secret, []any{}, "acme/infra"}}); res.OK || !res.Refused {
		t.Errorf("memory.remember of a secret must be refused: %#v", res)
	}

	// Writes AND reads are vetted (the #124 store allowlist needs every
	// touch), each carrying its store name; memory ops carry none.
	var sawWrite, sawRead bool
	for _, v := range vetted {
		switch v {
		case "kv.set@s", "sql.exec@db", "memory.remember@":
			sawWrite = true
		case "kv.get@s":
			sawRead = true
		}
	}
	if !sawWrite || !sawRead {
		t.Fatalf("the guard must see writes and reads with their store: %v", vetted)
	}
}

// A nil guard (no plan barrier) is the config-authored case and restricts
// nothing beyond the stores' own gates.
func TestNilDataGuardDoesNotRestrictConfigSteps(t *testing.T) {
	needSh(t)
	tempKV(t)
	helper := ctxHelperScript(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{
		Run: "cli", Command: []string{"sh"}, Code: `
"$HELPER" ctx kv s set ns loot "$LEAK" >/dev/null || exit 1
printf '{"got": %s}' "$("$HELPER" ctx kv s get ns loot)"
`,
		Env: map[string]string{"HELPER": helper, ctxClientHelperEnv: "1",
			"LEAK": "c0de-s3cr3t-value"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["got"] != "c0de-s3cr3t-value" {
		t.Fatalf("nil guard must not restrict config steps: %#v", out)
	}
}
