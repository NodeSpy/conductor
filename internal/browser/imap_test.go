package browser

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/browser/browsertest"
)

func fakeMailbox(t *testing.T, tlsCfg *tls.Config) *browsertest.Mailbox {
	t.Helper()
	mb := &browsertest.Mailbox{User: "agent-inbox@example.test", Pass: "app-password-not-secret"}
	if err := mb.Serve("127.0.0.1:0", tlsCfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mb.Close)
	return mb
}

var codeRE = regexp.MustCompile(`code is ([0-9]{6})`)

func TestEmailCodeReadsOnlyTheMatch(t *testing.T) {
	mb := fakeMailbox(t, nil)
	now := time.Now()
	mb.Deliver("Test Site <no-reply@testsite.example>", "Your sign-in code", "old: your code is 111111", now.Add(-time.Hour))
	mb.Deliver("Evil <no-reply@testsite.example.evil>", "Your sign-in code", "your code is 666666", now)
	mb.Deliver("Test Site <no-reply@testsite.example>", "Newsletter", "your code is 777777", now)
	mb.Deliver("Test Site <no-reply@testsite.example>", "Your sign-in code", "Your code is =\r\n222222.", now)
	box := Mailbox{Name: "inbox", Addr: mb.Addr(), PlainLoopback: true}
	ec := EmailCode{Mailbox: "inbox", From: "no-reply@testsite.example", Subject: "sign-in code", Code: codeRE, Wait: time.Second}
	ctx := context.Background()
	used := map[string]bool{}
	code, id, err := fetchEmailCode(ctx, box, ec, mb.User, mb.Pass, now.Add(-time.Minute), used)
	if err != nil || code != "222222" {
		t.Fatalf("the newest match after the step (quoted-printable decoded): %q %v", code, err)
	}
	// Read-only, and only the one body.
	bodies := 0
	for _, c := range mb.Log() {
		up := strings.ToUpper(c)
		if strings.Contains(up, " SELECT ") || strings.Contains(up, "STORE") || strings.Contains(up, "EXPUNGE") {
			t.Fatalf("a mailbox-changing command: %s", c)
		}
		if strings.Contains(c, "BODY.PEEK[])") {
			bodies++
		}
		if strings.Contains(c, mb.Pass) {
			t.Fatal("the app password is in the command log")
		}
	}
	if bodies != 1 {
		t.Fatalf("bodies read: %d (want only the match)", bodies)
	}
	// Single use: the same message is never filled twice; older ones never.
	used[id] = true
	if _, _, err := fetchEmailCode(ctx, box, ec, mb.User, mb.Pass, now.Add(-time.Minute), used); err == nil || !strings.Contains(err.Error(), "no message") {
		t.Fatalf("a used message again: %v", err)
	}
	// A sender at exactly a domain.
	ec.From = "@testsite.example"
	if code, _, err := fetchEmailCode(ctx, box, ec, mb.User, mb.Pass, now.Add(-time.Minute), nil); code != "222222" || err != nil {
		t.Fatalf("@domain: %q %v", code, err)
	}
	// No content in an error: a message without a code.
	mb.Deliver("no-reply@testsite.example", "Your sign-in code", "nothing here 999999 secret-ish text", time.Now())
	_, _, err = fetchEmailCode(ctx, box, ec, mb.User, mb.Pass, time.Now().Add(-time.Second), map[string]bool{id: true})
	if err == nil || strings.Contains(err.Error(), "secret-ish") || strings.Contains(err.Error(), "999999") {
		t.Fatalf("no code: %v", err)
	}
	// A wrong login says so, nothing more.
	if _, _, err := fetchEmailCode(ctx, box, ec, mb.User, "wrong-pass", now, nil); err == nil || !strings.Contains(err.Error(), "login refused") || strings.Contains(err.Error(), "wrong-pass") {
		t.Fatalf("wrong login: %v", err)
	}
}

func TestSenderMatches(t *testing.T) {
	for h, ok := range map[string]bool{
		"Test <no-reply@testsite.example>": true, "NO-REPLY@TESTSITE.EXAMPLE": true,
		"no-reply@testsite.example.evil": false, "x-no-reply@testsite.example": false,
		"<no-reply@testsite.example> evil": false, "": false,
	} {
		if senderMatches(h, "no-reply@testsite.example") != ok {
			t.Errorf("%q", h)
		}
	}
	for h, ok := range map[string]bool{"a@testsite.example": true, "a@sub.testsite.example": false, "a@testsite.example.evil": false} {
		if senderMatches(h, "@testsite.example") != ok {
			t.Errorf("@domain %q", h)
		}
	}
	if senderMatches("a@b", "") || senderMatches("a@b", "@") {
		t.Fatal("an empty matcher matches nothing")
	}
}

func TestEmailCodeTLS(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mail.test"}, DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	mb := fakeMailbox(t, &tls.Config{Certificates: []tls.Certificate{cert}})
	mb.Deliver("no-reply@testsite.example", "Your sign-in code", "your code is 424242", time.Now())
	_, port, _ := net.SplitHostPort(mb.Addr())
	ec := EmailCode{Mailbox: "inbox", From: "no-reply@testsite.example", Subject: "code", Code: codeRE, Wait: time.Second}
	box := Mailbox{Name: "inbox", Addr: "localhost:" + port, CAFile: ca}
	if code, _, err := fetchEmailCode(context.Background(), box, ec, mb.User, mb.Pass, time.Now().Add(-time.Minute), nil); err != nil || code != "424242" {
		t.Fatalf("TLS with the operator's CA: %q %v", code, err)
	}
	box.CAFile = ""
	if _, _, err := fetchEmailCode(context.Background(), box, ec, mb.User, mb.Pass, time.Now().Add(-time.Minute), nil); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("an untrusted certificate: %v", err)
	}
	// Plaintext only to loopback.
	if _, err := imapDial(context.Background(), Mailbox{Name: "x", Addr: "192.0.2.1:143", PlainLoopback: true}); err == nil {
		t.Fatal("plaintext to a remote mailbox")
	}
}
