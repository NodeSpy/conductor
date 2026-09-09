package sourcekit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifyHMAC(t *testing.T) {
	secret, body := "s3cret", []byte(`{"a":1}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	good := hex.EncodeToString(mac.Sum(nil))

	if !VerifyHMAC(secret, body, good) {
		t.Fatal("valid hex signature rejected")
	}
	if !VerifyHMAC(secret, body, "sha256="+good) {
		t.Fatal("valid sha256=-prefixed signature rejected")
	}
	if VerifyHMAC(secret, body, "deadbeef") {
		t.Fatal("bad signature accepted")
	}
	if VerifyHMAC(secret, body, "not-hex!!") {
		t.Fatal("non-hex signature accepted")
	}
	if !VerifyHMAC("", body, "anything") {
		t.Fatal("empty secret should disable verification")
	}
}

func TestDedup(t *testing.T) {
	d := NewDedup(2)
	if !d.Add("a") || !d.Add("b") {
		t.Fatal("new keys should be accepted")
	}
	if d.Add("a") {
		t.Fatal("duplicate key should be rejected")
	}
	// Adding a third evicts the oldest ("a"), so "a" is new again.
	d.Add("c")
	if !d.Add("a") {
		t.Fatal("evicted key should be new again")
	}
}
