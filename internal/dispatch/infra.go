package dispatch

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// INFRASTRUCTURE FAILURES.
//
// An agent turn can fail without the agent ever answering: the runtime could
// not reach its model (a TLS or connection failure, an auth failure, a rate
// limit, an overloaded or erroring API), it returned nothing at all, or its
// launch never started. What comes back in those cases is an error message
// from the runtime, not the agent's reply, and nothing about the step is
// wrong: run the same step against a working runtime and it succeeds.
//
// Conductor tells the two apart so it can recover on its own. An
// infrastructure failure does not spend the output_schema corrective turn
// (no prompt fixes a broken network), is not counted as the step's own
// failure, and puts the run into a wait that resumes it once the runtime
// works again (engine/infrawait.go).

// ErrInfrastructure marks a step that failed because its agent runtime did
// not work, not because of anything the agent did. errors.Is matches every
// *InfraError.
var ErrInfrastructure = errors.New("agent runtime unavailable")

// InfraError is an infrastructure failure: why the runtime did not work and
// what it printed instead of an answer. Runtime/Model/Provider name what the
// failed dispatch ran on, filled in by the flow once the dispatch returns, so
// the recovery can probe exactly that.
type InfraError struct {
	// Reason is the one-line cause, from the runtime's own words.
	Reason string
	// Reply is an excerpt of what the runtime printed in place of an answer
	// ("" when it printed nothing).
	Reply string

	Runtime, Model, Provider string

	cause error
}

func (e *InfraError) Error() string {
	msg := ErrInfrastructure.Error() + ": " + e.Reason
	if e.Reply != "" && !strings.Contains(e.Reason, e.Reply) {
		msg += " (runtime reply: " + e.Reply + ")"
	}
	if e.cause != nil && !strings.Contains(msg, e.cause.Error()) {
		msg += ": " + e.cause.Error()
	}
	return msg
}

func (e *InfraError) Unwrap() error { return e.cause }

func (e *InfraError) Is(target error) bool { return target == ErrInfrastructure }

// Infra builds an infrastructure failure. cause (may be nil) stays reachable
// through errors.Is/As, so an Unrecoverable launch failure still reads as one
// once the recovery gives up on it.
func Infra(reason, reply string, cause error) error {
	return &InfraError{Reason: Excerpt(reason), Reply: Excerpt(reply), cause: cause}
}

// AsInfra returns the infrastructure failure err carries, if any.
func AsInfra(err error) (*InfraError, bool) {
	var ie *InfraError
	if errors.As(err, &ie) {
		return ie, true
	}
	return nil, false
}

// IsInfra reports whether err is (or wraps) an infrastructure failure: an
// *InfraError, or an error from a layer that cannot import this package (a
// plugin's transport) that declares itself one (InfraMarker).
func IsInfra(err error) bool {
	if errors.Is(err, ErrInfrastructure) {
		return true
	}
	var m InfraMarker
	return errors.As(err, &m)
}

// InfraMarker is implemented by an error that is an infrastructure failure
// of something other than an agent runtime — a plugin that stopped
// answering, say. Infrastructure names what failed ("plugin <name>"): the
// recovery groups waiting runs by it. A marked failure has no cheap probe;
// the run's own step, re-run on a backoff, is the probe.
type InfraMarker interface {
	error
	Infrastructure() string
}

// InfraSubject is what a marked infrastructure failure names ("" for none).
func InfraSubject(err error) string {
	var m InfraMarker
	if errors.As(err, &m) {
		return m.Infrastructure()
	}
	return ""
}

// maxInfraReply bounds a reply that can still be a runtime error message: an
// error is a line or a few, an agent's answer that merely mentions an error
// is longer. Past this nothing is classified.
const (
	maxInfraReply      = 1500
	maxInfraReplyLines = 8
)

// infraLead is how a runtime error message starts: the reply's first line
// leads with an error word. An agent's prose that mentions a rate limit in
// passing does not.
var infraLead = regexp.MustCompile(`(?i)^\W*(api error|error|fatal|failed|failure|unable to|could not|cannot connect|can't connect|connection|request failed|network|authentication|unauthori[sz]ed|forbidden|rate.?limit|too many requests|overloaded|service unavailable|bad gateway|gateway timeout|internal server error|timed out|timeout|invalid api key|credit balance|quota)\b`)

// infraSignal is what in such a message names a transport, auth or capacity
// failure rather than, say, a tool error inside the agent's work.
var infraSignal = regexp.MustCompile(`(?i)(unable to connect|could not connect|cannot connect|can't connect|connection (refused|reset|closed|error|failed|timed out)|econnrefused|econnreset|etimedout|enotfound|eai_again|no such host|network is unreachable|dns|tls|ssl|x509|certificate|handshake|socket hang up|proxy|timed out|timeout|rate.?limit|too many requests|overloaded|capacity|quota|credit balance|unauthori[sz]ed|authentication|invalid api key|api key|forbidden|service unavailable|bad gateway|gateway timeout|internal server error|\b(401|403|408|429|500|502|503|504|529)\b)`)

// ClassifyReply reports whether an agent turn's reply is really its runtime's
// error message (or nothing at all) rather than an answer, returning the
// infrastructure failure when it is and nil otherwise. It is deliberately
// narrow — a short reply that LEADS with an error and names a transport, auth
// or capacity failure — because a false positive re-runs the step: a real
// answer must never read as one. A reply carrying a JSON object is an answer.
func ClassifyReply(reply string) *InfraError {
	s := strings.TrimSpace(reply)
	if s == "" {
		return &InfraError{Reason: "the agent runtime returned no output"}
	}
	if len(s) > maxInfraReply || strings.Count(s, "\n") >= maxInfraReplyLines || strings.Contains(s, "{\"") {
		return nil
	}
	first := firstLine(s)
	if !infraLead.MatchString(first) || !infraSignal.MatchString(s) {
		return nil
	}
	return &InfraError{Reason: Excerpt(first), Reply: Excerpt(s)}
}

// ClassifyText reports whether a runtime's error text (a failed turn, a
// launch that did not start) names a transport, auth or capacity failure.
// Unlike ClassifyReply it does not require the text to lead with an error
// word: it is already known to be an error.
func ClassifyText(text string) *InfraError {
	s := strings.TrimSpace(text)
	if s == "" || len(s) > 4*maxInfraReply || !infraSignal.MatchString(s) {
		return nil
	}
	return &InfraError{Reason: Excerpt(firstLine(s))}
}

// maxExcerpt bounds what of a reply rides on an error, an audit row or a run
// record: enough to say why, never a transcript.
const maxExcerpt = 400

// credentialShape matches the common ways a credential is spelled out in an
// error message: an Authorization header, a key=value / key: value pair
// naming a token, key, secret or password, and a URL's user:password@.
var credentialShape = regexp.MustCompile(`(?i)(bearer\s+|basic\s+|(?:token|api[_-]?key|apikey|secret|password|passwd|authorization|x-api-key|access[_-]?key)["']?\s*[:=]\s*["']?(?:bearer\s+|basic\s+)?)[^\s"',;]+|(://[^/\s:@]+:)[^@\s/]+@`)

// Excerpt is the form a reply takes on an error, an audit row or a run
// record: whitespace collapsed, credential-shaped values masked, clipped to
// a few hundred characters. The flow's own redaction of known secret values
// still applies on top.
func Excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = credentialShape.ReplaceAllStringFunc(s, func(m string) string {
		sub := credentialShape.FindStringSubmatch(m)
		switch {
		case sub[1] != "":
			return sub[1] + "[REDACTED]"
		case sub[2] != "":
			return sub[2] + "[REDACTED]@"
		}
		return "[REDACTED]"
	})
	if len(s) <= maxExcerpt {
		return s
	}
	cut := maxExcerpt
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
