package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/secrets"
)

// REGRESSION: logf wrote straight to stderr with no redaction — and it is
// the ONE logger handed to engine/flow/dispatch/notify/affinity/memory/
// handoffs/control, so any tracked secret reaching any subsystem's log line
// landed in the journal cleartext. logf now redacts at the choke point.
func TestLogfRedactsSecrets(t *testing.T) {
	const secret = "logf-s3cr3t-XYZZY"
	res := secrets.New()
	res.Track(secret)
	setLogRedactor(res)
	t.Cleanup(func() { setLogRedactor(nil) })

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	logf("dispatch failed: token %s rejected (url https://api.example/?key=%s)", secret, secret)
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)

	if strings.Contains(string(out), secret) {
		t.Fatalf("secret reached the journal: %s", out)
	}
	if !strings.Contains(string(out), secrets.Placeholder) {
		t.Fatalf("journal line should carry the placeholder: %s", out)
	}
	// Without a resolver (legacy configs), logf still logs.
	setLogRedactor(nil)
	r2, w2, _ := os.Pipe()
	os.Stderr = w2
	logf("plain %s", "line")
	w2.Close()
	os.Stderr = old
	out2, _ := io.ReadAll(r2)
	if !strings.Contains(string(out2), "plain line") {
		t.Fatalf("nil-resolver logf must pass through: %s", out2)
	}
}
