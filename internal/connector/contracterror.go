package connector

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/NodeSpy/conductor/internal/acp"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// ContractError carries a plugin's JSON-RPC error ANSWER (the plugin is up
// and answered; this is never a transport failure or a timeout — those tear
// the subprocess down instead, internal/plugin/client.go callFor, and are
// never contract errors) through the engine as a typed Go error, so flow and
// engine code switches on Code instead of pattern-matching a string.
// docs/design/plugin-contract.md §1.11 is the source of truth for what each
// code means and how the engine acts on it.
//
// It is produced once, at the connector boundary (external.go invokePlugin),
// from the acp.RPCError the transport decoded off the wire, and from there
// travels as an ordinary wrapped error (%w, never %v/%s) so errors.As finds
// it under any number of "uses %s: %w"-style wraps.
type ContractError struct {
	Code    int
	Message string
	// Data is the error's structured payload, as the plugin sent it:
	// {status, retryable} for upstream, {retry_after} for rate_limited.
	Data map[string]any
}

func (e *ContractError) Error() string {
	if len(e.Data) > 0 {
		return fmt.Sprintf("plugin error %d: %s %v", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("plugin error %d: %s", e.Code, e.Message)
}

// contractErrorFrom converts an answered JSON-RPC error into a
// *ContractError. Anything that is not an *acp.RPCError (a transport
// failure, a timeout, a plain Go error raised elsewhere in the call) passes
// through unchanged — only an answer the plugin itself sent back is a
// contract error.
func contractErrorFrom(err error) error {
	var re *acp.RPCError
	if !errors.As(err, &re) {
		return err
	}
	ce := &ContractError{Code: re.Code, Message: re.Message}
	if len(re.Data) > 0 {
		_ = json.Unmarshal(re.Data, &ce.Data)
	}
	return ce
}

// AsContractError reports whether err is, or wraps, a *ContractError. Every
// call site that acts on §1.11 behavior goes through this (never a bare type
// assertion), so a wrapped error ("uses %s: %w", a stepError, errors.Join …)
// still resolves.
func AsContractError(err error) (*ContractError, bool) {
	var ce *ContractError
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}

// IsTargetGone, IsInvalid, IsRateLimited, IsNotReady and IsUpstream report
// the plugin's declared code (§1.11 table). Nil-safe, so a caller can chain
// off a *ContractError that might be nil without an extra check.
func (e *ContractError) IsTargetGone() bool  { return e != nil && e.Code == sdk.CodeTargetGone }
func (e *ContractError) IsInvalid() bool     { return e != nil && e.Code == sdk.CodeInvalid }
func (e *ContractError) IsRateLimited() bool { return e != nil && e.Code == sdk.CodeRateLimited }
func (e *ContractError) IsNotReady() bool    { return e != nil && e.Code == sdk.CodeNotReady }
func (e *ContractError) IsUpstream() bool    { return e != nil && e.Code == sdk.CodeUpstream }

// TargetKey reports CodeTargetGone's data.target — the key of the target the
// CALL addressed (plugin-contract.md §1.11, finding 11), the same string a
// target.key the plugin's own events carry. Absent (an older or careless
// plugin) reports ok=false.
func (e *ContractError) TargetKey() (key string, ok bool) {
	if e == nil {
		return "", false
	}
	s, _ := e.Data["target"].(string)
	return s, s != ""
}

// TargetGoneMatchesKey reports whether a target_gone answer's data.target
// names the SAME target as key — the only case a caller may honor it as a
// stop (finding 11). key is the run's own trigger target key
// (core.Trigger.Key()): a target_gone answered for some OTHER target the
// call happened to touch (a missing Slack notification channel a hook posts
// to, say) must never be read as "this run's own target is gone" just
// because the CODE matches — that would silently stop an unrelated run (a PR
// review, a deploy) over a problem with something it merely notifies, not
// what it is about. An absent data.target, or one naming a different target,
// both report false; the caller turns those into a loud, non-retryable
// upstream failure instead (TargetGoneOrUpstream).
func (e *ContractError) TargetGoneMatchesKey(key string) bool {
	if !e.IsTargetGone() || key == "" {
		return false
	}
	got, ok := e.TargetKey()
	return ok && got == key
}

// TargetGoneOrUpstream is the shared finding-11 gate every target_gone
// interpreter applies: err passes through completely UNCHANGED unless it is
// a target_gone contract error. A target_gone is honored as a stop
// (isStop=true, result is err itself — the caller wraps it as its own stop
// sentinel, e.g. dispatch.ErrTargetClosed) ONLY when its data.target matches
// key (TargetGoneMatchesKey). A target_gone that does NOT match — wrong
// target, or no target named at all — comes back as a loud, NON-RETRYABLE
// upstream failure instead (isStop=false), carrying the plugin's own
// message: never silently dropped, and never reinterpreted as target_gone
// again by anything further down the stack (noStepRetry/execWithRetry
// already refuse to retry a non-retryable upstream answer regardless of the
// step's own retry:).
func TargetGoneOrUpstream(err error, key string) (result error, isStop bool) {
	ce, ok := AsContractError(err)
	if !ok || !ce.IsTargetGone() {
		return err, false
	}
	if ce.TargetGoneMatchesKey(key) {
		return err, true
	}
	got, _ := ce.TargetKey()
	var msg string
	if got != "" {
		msg = fmt.Sprintf("target_gone for a different target (%q) than this run's own (%q): %s", got, key, ce.Message)
	} else {
		msg = fmt.Sprintf("target_gone named no target (this run's own is %q): %s", key, ce.Message)
	}
	return &ContractError{Code: sdk.CodeUpstream, Message: msg, Data: map[string]any{"retryable": false}}, false
}

// RetryAfter parses CodeRateLimited's data.retry_after (a Go duration
// string, e.g. "90s"). ok is false when it is absent or unparseable.
func (e *ContractError) RetryAfter() (d time.Duration, ok bool) {
	if e == nil {
		return 0, false
	}
	s, _ := e.Data["retry_after"].(string)
	if s == "" {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	return d, err == nil
}

// UpstreamRetryable reports CodeUpstream's data.retryable — false (never
// retried) when it is absent, so a plugin that forgets the field fails
// closed rather than retrying blind.
func (e *ContractError) UpstreamRetryable() bool {
	if e == nil {
		return false
	}
	b, _ := e.Data["retryable"].(bool)
	return b
}

// UpstreamStatus reports CodeUpstream's data.status (the upstream's own
// status code), when present.
func (e *ContractError) UpstreamStatus() (int, bool) {
	if e == nil {
		return 0, false
	}
	switch v := e.Data["status"].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}
