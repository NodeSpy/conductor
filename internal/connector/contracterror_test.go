package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/acp"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/plugin"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// contractErrorFrom must convert every answered JSON-RPC error into a
// *ContractError carrying its code/message/data, and must leave anything
// that is NOT an answered error (a transport failure, a plain Go error)
// untouched — those never carry §1.11 semantics.
func TestContractErrorFromRPCError(t *testing.T) {
	cases := []struct {
		name string
		code int
		data string
	}{
		{"upstream", sdk.CodeUpstream, `{"status":502,"retryable":true}`},
		{"target_gone", sdk.CodeTargetGone, ``},
		{"invalid", sdk.CodeInvalid, ``},
		{"rate_limited", sdk.CodeRateLimited, `{"retry_after":"30s"}`},
		{"not_ready", sdk.CodeNotReady, ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var raw json.RawMessage
			if c.data != "" {
				raw = json.RawMessage(c.data)
			}
			in := &acp.RPCError{Code: c.code, Message: "boom", Data: raw}
			out := contractErrorFrom(in)
			ce, ok := AsContractError(out)
			if !ok {
				t.Fatalf("contractErrorFrom(%+v) = %v (%T), want a *ContractError", in, out, out)
			}
			if ce.Code != c.code || ce.Message != "boom" {
				t.Fatalf("got Code=%d Message=%q, want %d %q", ce.Code, ce.Message, c.code, "boom")
			}
		})
	}
	t.Run("transport failure passes through unchanged", func(t *testing.T) {
		plain := errors.New("connection reset")
		if out := contractErrorFrom(plain); out != plain {
			t.Fatalf("transport error was altered: %v", out)
		}
		if _, ok := AsContractError(plain); ok {
			t.Fatal("a plain error must never look like a contract error")
		}
	})
	t.Run("wrapped RPCError still resolves", func(t *testing.T) {
		in := &acp.RPCError{Code: sdk.CodeTargetGone, Message: "gone"}
		wrapped := fmt.Errorf("uses x.y: %w", in)
		ce, ok := AsContractError(contractErrorFrom(wrapped))
		if !ok || !ce.IsTargetGone() {
			t.Fatalf("wrapped RPCError not recognized: %v", ce)
		}
	})
}

func TestContractErrorPredicatesAndData(t *testing.T) {
	up := &ContractError{Code: sdk.CodeUpstream, Data: map[string]any{"status": float64(503), "retryable": true}}
	if !up.IsUpstream() || up.IsInvalid() || up.IsTargetGone() {
		t.Fatalf("upstream predicate mismatch: %+v", up)
	}
	if !up.UpstreamRetryable() {
		t.Fatal("want retryable=true")
	}
	if status, ok := up.UpstreamStatus(); !ok || status != 503 {
		t.Fatalf("UpstreamStatus() = %d, %v", status, ok)
	}
	noData := &ContractError{Code: sdk.CodeUpstream}
	if noData.UpstreamRetryable() {
		t.Fatal("an upstream error with no retryable field must fail closed (never retried)")
	}
	rl := &ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "90s"}}
	if !rl.IsRateLimited() {
		t.Fatal("want rate_limited")
	}
	d, ok := rl.RetryAfter()
	if !ok || d != 90*time.Second {
		t.Fatalf("RetryAfter() = %v, %v", d, ok)
	}
	bad := &ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "not-a-duration"}}
	if _, ok := bad.RetryAfter(); ok {
		t.Fatal("an unparseable retry_after must answer ok=false")
	}
	gone := &ContractError{Code: sdk.CodeTargetGone}
	if !gone.IsTargetGone() || gone.IsNotReady() {
		t.Fatalf("target_gone predicate mismatch: %+v", gone)
	}
	inv := &ContractError{Code: sdk.CodeInvalid}
	if !inv.IsInvalid() {
		t.Fatal("want invalid")
	}
	nr := &ContractError{Code: sdk.CodeNotReady}
	if !nr.IsNotReady() {
		t.Fatal("want not_ready")
	}
	var nilCE *ContractError
	if nilCE.IsTargetGone() || nilCE.IsInvalid() || nilCE.IsUpstream() || nilCE.IsRateLimited() || nilCE.IsNotReady() {
		t.Fatal("a nil *ContractError must answer false to every predicate")
	}
}

// rpcErrInvoker answers a scripted sequence of (out, err) pairs, one per
// call, repeating the last entry once exhausted — the shape every bounded-
// retry test below scripts against.
type rpcErrInvoker struct {
	seq   []invokeAnswer
	calls int
}

type invokeAnswer struct {
	out map[string]any
	err error
}

func (f *rpcErrInvoker) Invoke(context.Context, plugin.InvokeRequest) (map[string]any, error) {
	i := f.calls
	if i >= len(f.seq) {
		i = len(f.seq) - 1
	}
	f.calls++
	return f.seq[i].out, f.seq[i].err
}

func rpcErr(code int, data string) *acp.RPCError {
	var raw json.RawMessage
	if data != "" {
		raw = json.RawMessage(data)
	}
	return &acp.RPCError{Code: code, Message: "plugin said so", Data: raw}
}

// externalImpl.Invoke must convert every answered JSON-RPC error into a
// *ContractError the caller can switch on — this is the external.go
// invokePlugin boundary (plugin-contract.md §1.11), the one place a plugin's
// error answer enters conductor's own types.
func TestExternalImplInvokeConvertsContractError(t *testing.T) {
	decl := mapDecl(&sdk.Decl{Type: "forge", Verbs: []sdk.Verb{{Name: "comment"}}})
	cases := []struct {
		name string
		code int
		data string
		want func(*ContractError) bool
	}{
		{"upstream", sdk.CodeUpstream, `{"status":500,"retryable":false}`, (*ContractError).IsUpstream},
		{"target_gone", sdk.CodeTargetGone, ``, (*ContractError).IsTargetGone},
		{"invalid", sdk.CodeInvalid, ``, (*ContractError).IsInvalid},
		{"not_ready", sdk.CodeNotReady, ``, (*ContractError).IsNotReady},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inv := &rpcErrInvoker{seq: []invokeAnswer{{err: rpcErr(c.code, c.data)}}}
			e := &externalImpl{client: inv, instance: "x", decl: decl, pluginType: "forge"}
			_, err := e.Invoke(context.Background(), "comment", nil)
			ce, ok := AsContractError(err)
			if !ok || !c.want(ce) {
				t.Fatalf("Invoke error = %v, want a matching *ContractError", err)
			}
		})
	}
}

// Target head read (reads_revision, §2.3): -32011 target_gone is PREFERRED
// over a failed read's blank/unknown state (plugin-contract.md §1.11 item
// 3) — the plugin is telling us directly, so TargetHead reports it as a
// closed target with no error, rather than swallowing it into "".
func TestTargetHeadPrefersTargetGoneCode(t *testing.T) {
	decl := mapDecl(&sdk.Decl{Type: "forge", Verbs: []sdk.Verb{{Name: "pr_head", Semantics: &sdk.VerbSemantics{
		HostOnly: true,
		ReadsRevision: &sdk.ReadsRevision{Revision: "sha", State: "state",
			States: map[string][]string{"open": {"open"}}, Reasons: map[string]string{TargetClosed: "the PR closed"}},
	}}}})
	inv := &rpcErrInvoker{seq: []invokeAnswer{{err: rpcErr(sdk.CodeTargetGone, "")}}}
	in := &Instance{Name: "gh", Decl: decl, Enabled: true,
		Impl: &externalImpl{client: inv, decl: decl, instance: "gh"}}
	tr := core.Trigger{Instance: "gh", TargetTrusted: true, Target: core.Target{Repo: "o/r", Number: 1}}
	h, err := in.TargetHead(context.Background(), tr)
	if err != nil {
		t.Fatalf("TargetHead returned an error for target_gone, want the state preferred instead: %v", err)
	}
	if h.State != TargetClosed || h.StopReason != "the PR closed" {
		t.Fatalf("TargetHead = %+v, want State=%q StopReason=%q", h, TargetClosed, "the PR closed")
	}
}

// reads_revision's own invoke is retried the same bounded way as any other
// (not_ready/rate_limited, §1.11) before TargetHead gives up and surfaces an
// error.
func TestTargetHeadRetriesNotReadyThenSucceeds(t *testing.T) {
	restoreSleep := Sleep
	Sleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { Sleep = restoreSleep })

	decl := mapDecl(&sdk.Decl{Type: "forge", Verbs: []sdk.Verb{{Name: "pr_head", Semantics: &sdk.VerbSemantics{
		HostOnly:      true,
		ReadsRevision: &sdk.ReadsRevision{Revision: "sha", State: "state", States: map[string][]string{"open": {"open"}}},
	}}}})
	inv := &rpcErrInvoker{seq: []invokeAnswer{
		{err: rpcErr(sdk.CodeNotReady, "")},
		{err: rpcErr(sdk.CodeNotReady, "")},
		{out: map[string]any{"sha": "deadbeef", "state": "open"}},
	}}
	in := &Instance{Name: "gh", Decl: decl, Enabled: true,
		Impl: &externalImpl{client: inv, decl: decl, instance: "gh"}}
	tr := core.Trigger{Instance: "gh", TargetTrusted: true, Target: core.Target{Repo: "o/r", Number: 1}}
	h, err := in.TargetHead(context.Background(), tr)
	if err != nil || h.SHA != "deadbeef" || inv.calls != 3 {
		t.Fatalf("TargetHead = %+v err=%v calls=%d, want 3 calls ending in success", h, err, inv.calls)
	}
}
