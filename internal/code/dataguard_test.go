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
// refused. The bindings now consult Spec.DataGuard before every durable
// write; reads and non-write ops stay unguarded.
func TestDataGuardBlocksBindingWrites(t *testing.T) {
	const secret = "c0de-s3cr3t-value"
	var vetted []string
	guard := func(kind, op string, args []any) error {
		vetted = append(vetted, kind+"."+op)
		if strings.Contains(fmt.Sprint(args...), secret) {
			return fmt.Errorf("no_secret_egress: refusing to write secret material into %s.%s", kind, op)
		}
		return nil
	}
	e := &Executor{}

	// kv via js: the secret write is refused; the store stays clean; a plain
	// write on the same guard passes.
	tempKV(t)
	_, err := e.Exec(context.Background(), Spec{Run: "js", DataGuard: guard, Code: `
ctx.store("s").set("ns", "loot", ctx.leak);
return 1`}, map[string]any{"leak": secret})
	if err == nil || !strings.Contains(err.Error(), "no_secret_egress") {
		t.Fatalf("js kv.set of a secret must be refused: %v", err)
	}
	if _, err := e.Exec(context.Background(), Spec{Run: "js", DataGuard: guard, Code: `
ctx.store("s").set("ns", "note", "plain");
return ctx.store("s").get("ns", "note")`}, nil); err != nil {
		t.Fatalf("plain writes and reads must pass: %v", err)
	}

	// sql via go-embed: exec with the secret bound is refused.
	tempSQL(t)
	_, err = e.Exec(context.Background(), Spec{Run: "go-embed", DataGuard: guard, Code: `
import "conductor/sql"

func run(ctx map[string]any) (any, error) {
	db, err := sql.Use("db")
	if err != nil {
		return nil, err
	}
	return db.Exec("INSERT INTO events (body) VALUES (?)", []any{ctx["leak"]})
}`}, map[string]any{"leak": secret})
	if err == nil || !strings.Contains(err.Error(), "no_secret_egress") {
		t.Fatalf("go-embed sql.exec of a secret must be refused: %v", err)
	}

	// memory via risor: remember with the secret is refused.
	tempMem(t)
	_, err = e.Exec(context.Background(), Spec{Run: "risor", DataGuard: guard, Code: `
memory.remember(ctx.leak, [], "global")`}, map[string]any{"leak": secret})
	if err == nil || !strings.Contains(err.Error(), "no_secret_egress") {
		t.Fatalf("risor memory.remember of a secret must be refused: %v", err)
	}

	// lua kv write path is guarded too.
	tempKV(t)
	_, err = e.Exec(context.Background(), Spec{Run: "lua", DataGuard: guard, Code: `
ctx.store("s").set("ns", "loot", ctx.leak)`}, map[string]any{"leak": secret})
	if err == nil || !strings.Contains(err.Error(), "no_secret_egress") {
		t.Fatalf("lua kv.set of a secret must be refused: %v", err)
	}

	var sawWrite bool
	for _, v := range vetted {
		if v == "kv.set" || v == "sql.exec" || v == "memory.remember" {
			sawWrite = true
		}
		if strings.HasSuffix(v, ".get") || strings.HasSuffix(v, ".query") || strings.HasSuffix(v, ".recall") {
			t.Fatalf("reads must not be guard-vetted: %v", vetted)
		}
	}
	if !sawWrite {
		t.Fatalf("the guard never saw a write: %v", vetted)
	}

	// A nil guard (no plan barrier) leaves everything as before.
	tempKV(t)
	if _, err := e.Exec(context.Background(), Spec{Run: "js", Code: `
ctx.store("s").set("ns", "loot", ctx.leak); return 1`}, map[string]any{"leak": secret}); err != nil {
		t.Fatalf("nil guard must not restrict config steps: %v", err)
	}
}
