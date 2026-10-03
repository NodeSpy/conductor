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
