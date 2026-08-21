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
	sigValue = strings.TrimSpace(sigValue)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sum := mac.Sum(nil)
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
