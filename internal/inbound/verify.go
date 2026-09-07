package inbound

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// VerifyHMAC checks an HMAC-SHA256 signature of body against secret. The scheme
// controls how sigValue is decoded:
//   - "" or "hex" or "sha256": hex digest, with an optional "sha256=" prefix
//     (GitHub/Sentry style).
//   - "base64": standard base64 digest.
//
// An empty secret means "no verification configured" and returns true, so callers
// can gate unconditionally; callers that want to *require* a signature should only
// invoke this when a secret is set.
func VerifyHMAC(secret string, body []byte, sigValue, scheme string) bool {
	if secret == "" {
		return true
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return equalSig(mac.Sum(nil), sigValue, scheme)
}

// SignedRequest builds the canonical string a callable HMAC caller signs (#36
// §13 review, item 4). Unlike a webhook signature — which external services
// like GitHub/Sentry compute over the body alone — a callable caller signs the
// timestamp, method, and path too, so a captured signature is bound to one
// endpoint at one moment and cannot be replayed against a different path (e.g.
// reusing a POST /invoke signature on GET /runs) or after the skew window.
// The format is newline-delimited with a stable field order:
//
//	<unix-timestamp>\n<METHOD>\n<path>\n<body>
func SignedRequest(timestamp, method, path string, body []byte) []byte {
	head := timestamp + "\n" + method + "\n" + path + "\n"
	out := make([]byte, 0, len(head)+len(body))
	out = append(out, head...)
	out = append(out, body...)
	return out
}

// VerifySignedRequest checks an HMAC-SHA256 signature over the SignedRequest
// canonical string. It returns false for an empty secret — a callable signer
// must have one (an unset secret is never an implicit pass, unlike VerifyHMAC's
// webhook semantics). Timestamp-skew and replay checks are the caller's
// responsibility: they need a clock and per-process state.
func VerifySignedRequest(secret, timestamp, method, path string, body []byte, sigValue, scheme string) bool {
	if secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(SignedRequest(timestamp, method, path, body))
	return equalSig(mac.Sum(nil), sigValue, scheme)
}

// equalSig decodes sigValue per scheme and constant-time compares it to sum.
func equalSig(sum []byte, sigValue, scheme string) bool {
	sigValue = strings.TrimSpace(sigValue)
	switch scheme {
	case "base64":
		want, err := base64.StdEncoding.DecodeString(sigValue)
		return err == nil && hmac.Equal(want, sum)
	default: // "", "hex", "sha256"
		sigValue = strings.TrimPrefix(sigValue, "sha256=")
		want, err := hex.DecodeString(sigValue)
		return err == nil && hmac.Equal(want, sum)
	}
}
