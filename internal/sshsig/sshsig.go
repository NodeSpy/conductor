// Package sshsig implements the OpenSSH "SSHSIG" signature format described
// in openssh-portable's PROTOCOL.sshsig: the wire format produced by
// `ssh-keygen -Y sign` and consumed by `ssh-keygen -Y verify`, and the format
// git writes when a repository is configured with `gpg.format=ssh`.
//
// It exists so conductor can sign (and, for tests and defense in depth,
// verify) commits with an operator's existing SSH key material — an
// ssh-agent identity or an unencrypted key on disk — without shelling out to
// ssh-keygen at commit time and without ever touching, logging, or returning
// private key bytes.
//
// The envelope this package produces/consumes is:
//
//	byte[6]   MAGIC_PREAMBLE = "SSHSIG"
//	uint32    SIG_VERSION (1)
//	string    publickey     (wire-encoded ssh.PublicKey.Marshal())
//	string    namespace
//	string    reserved      (currently always empty)
//	string    hash_algorithm ("sha256" or "sha512")
//	string    signature      (itself: string sig_format, string sig_blob)
//
// armored between "-----BEGIN SSH SIGNATURE-----" and
// "-----END SSH SIGNATURE-----" lines as base64 wrapped at 70 columns. The
// data actually run through the signing key is:
//
//	byte[6]   MAGIC_PREAMBLE
//	string    namespace
//	string    reserved
//	string    hash_algorithm
//	string    H(message)
//
// where H is the named hash_algorithm applied to the message being signed.
// RSA keys are always signed with rsa-sha2-512 (never the SHA-1 "ssh-rsa"
// algorithm), matching ssh-keygen's default and git's expectations.
package sshsig

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	magic         = "SSHSIG"
	sigVersion    = 1
	hashAlgorithm = "sha512" // fixed per the Sign API contract; Verify also accepts sha256 for interop.

	armorHeader = "-----BEGIN SSH SIGNATURE-----"
	armorFooter = "-----END SSH SIGNATURE-----"
	armorWidth  = 70
)

// wireWriter builds the length-prefixed "string" fields used throughout the
// SSHSIG envelope (RFC 4251 §5: uint32 length followed by that many bytes),
// plus the occasional raw field (the magic preamble, the version number).
type wireWriter struct{ buf bytes.Buffer }

func (w *wireWriter) raw(b []byte) { w.buf.Write(b) }

func (w *wireWriter) uint32(v uint32) {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], v)
	w.buf.Write(lenBuf[:])
}

func (w *wireWriter) string(b []byte) {
	w.uint32(uint32(len(b)))
	w.buf.Write(b)
}

func (w *wireWriter) bytes() []byte { return w.buf.Bytes() }

// wireReader is the inverse of wireWriter, reading off the front of a byte
// slice without allocating (a malformed length just runs out of input,
// rather than attempting a huge allocation).
type wireReader struct{ b []byte }

var errTruncated = errors.New("sshsig: truncated signature")

func (r *wireReader) raw(n int) ([]byte, error) {
	if n < 0 || len(r.b) < n {
		return nil, errTruncated
	}
	out := r.b[:n]
	r.b = r.b[n:]
	return out, nil
}

func (r *wireReader) uint32() (uint32, error) {
	b, err := r.raw(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func (r *wireReader) string() ([]byte, error) {
	n, err := r.uint32()
	if err != nil {
		return nil, err
	}
	return r.raw(int(n))
}

// Sign returns the armored SSHSIG signature of message under namespace,
// using the fixed hash algorithm "sha512" (ssh-keygen's own default). RSA
// signers must implement ssh.AlgorithmSigner so the signature can be made
// with rsa-sha2-512; a plain ssh-rsa (SHA-1) signer is rejected.
func Sign(signer ssh.Signer, namespace string, message []byte) ([]byte, error) {
	if signer == nil {
		return nil, errors.New("sshsig: signer is nil")
	}
	if namespace == "" {
		return nil, errors.New("sshsig: namespace must not be empty")
	}

	digest := sha512.Sum512(message)

	var toSign wireWriter
	toSign.raw([]byte(magic))
	toSign.string([]byte(namespace))
	toSign.string(nil) // reserved
	toSign.string([]byte(hashAlgorithm))
	toSign.string(digest[:])

	sig, err := signBlob(signer, toSign.bytes())
	if err != nil {
		return nil, fmt.Errorf("sshsig: sign: %w", err)
	}

	var sigField wireWriter
	sigField.string([]byte(sig.Format))
	sigField.string(sig.Blob)

	var env wireWriter
	env.raw([]byte(magic))
	env.uint32(sigVersion)
	env.string(signer.PublicKey().Marshal())
	env.string([]byte(namespace))
	env.string(nil) // reserved
	env.string([]byte(hashAlgorithm))
	env.string(sigField.bytes())

	return armor(env.bytes()), nil
}

// signBlob signs data with signer, forcing rsa-sha2-512 for RSA keys since
// ssh.Signer.Sign on a plain RSA key would otherwise fall back to the
// deprecated SHA-1 "ssh-rsa" algorithm, which ssh-keygen/git no longer
// accept for SSHSIG.
func signBlob(signer ssh.Signer, data []byte) (*ssh.Signature, error) {
	if signer.PublicKey().Type() != ssh.KeyAlgoRSA {
		return signer.Sign(rand.Reader, data)
	}
	algSigner, ok := signer.(ssh.AlgorithmSigner)
	if !ok {
		return nil, errors.New("rsa signer does not support rsa-sha2-512, and plain ssh-rsa (SHA-1) signatures are not permitted")
	}
	return algSigner.SignWithAlgorithm(rand.Reader, data, ssh.KeyAlgoRSASHA512)
}

// Verify checks an armored SSHSIG signature against message and namespace,
// and returns the public key that produced it. The namespace must match
// exactly (git uses "git"; ssh-keygen's own commands default to "file").
func Verify(armored []byte, namespace string, message []byte) (ssh.PublicKey, error) {
	blob, err := dearmor(armored)
	if err != nil {
		return nil, err
	}

	r := &wireReader{b: blob}

	gotMagic, err := r.raw(len(magic))
	if err != nil {
		return nil, fmt.Errorf("sshsig: %w", err)
	}
	if string(gotMagic) != magic {
		return nil, errors.New("sshsig: not an SSHSIG blob (bad magic)")
	}

	version, err := r.uint32()
	if err != nil {
		return nil, fmt.Errorf("sshsig: %w", err)
	}
	if version != sigVersion {
		return nil, fmt.Errorf("sshsig: unsupported version %d", version)
	}

	pubKeyBytes, err := r.string()
	if err != nil {
		return nil, fmt.Errorf("sshsig: read public key: %w", err)
	}
	ns, err := r.string()
	if err != nil {
		return nil, fmt.Errorf("sshsig: read namespace: %w", err)
	}
	reserved, err := r.string()
	if err != nil {
		return nil, fmt.Errorf("sshsig: read reserved field: %w", err)
	}
	hashAlg, err := r.string()
	if err != nil {
		return nil, fmt.Errorf("sshsig: read hash algorithm: %w", err)
	}
	sigField, err := r.string()
	if err != nil {
		return nil, fmt.Errorf("sshsig: read signature: %w", err)
	}

	if string(ns) != namespace {
		return nil, fmt.Errorf("sshsig: namespace mismatch: signature is for %q, want %q", ns, namespace)
	}

	pubKey, err := ssh.ParsePublicKey(pubKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("sshsig: parse public key: %w", err)
	}

	sr := &wireReader{b: sigField}
	sigFormat, err := sr.string()
	if err != nil {
		return nil, fmt.Errorf("sshsig: read signature format: %w", err)
	}
	sigBlob, err := sr.string()
	if err != nil {
		return nil, fmt.Errorf("sshsig: read signature blob: %w", err)
	}

	digest, err := hashMessage(string(hashAlg), message)
	if err != nil {
		return nil, err
	}

	var toVerify wireWriter
	toVerify.raw([]byte(magic))
	toVerify.string(ns)
	toVerify.string(reserved)
	toVerify.string(hashAlg)
	toVerify.string(digest)

	sig := &ssh.Signature{Format: string(sigFormat), Blob: sigBlob}
	if err := pubKey.Verify(toVerify.bytes(), sig); err != nil {
		return nil, fmt.Errorf("sshsig: signature verification failed: %w", err)
	}
	return pubKey, nil
}

// hashMessage applies the named SSHSIG hash algorithm to message. Only the
// two algorithms defined by PROTOCOL.sshsig are accepted.
func hashMessage(alg string, message []byte) ([]byte, error) {
	switch alg {
	case "sha256":
		h := sha256.Sum256(message)
		return h[:], nil
	case "sha512":
		h := sha512.Sum512(message)
		return h[:], nil
	default:
		return nil, fmt.Errorf("sshsig: unsupported hash algorithm %q", alg)
	}
}

// armor wraps a binary SSHSIG blob in the PEM-like block ssh-keygen and git
// expect, base64-encoded and wrapped at armorWidth columns.
func armor(blob []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(blob)

	var out bytes.Buffer
	out.WriteString(armorHeader)
	out.WriteByte('\n')
	for i := 0; i < len(enc); i += armorWidth {
		end := i + armorWidth
		if end > len(enc) {
			end = len(enc)
		}
		out.WriteString(enc[i:end])
		out.WriteByte('\n')
	}
	out.WriteString(armorFooter)
	out.WriteByte('\n')
	return out.Bytes()
}

// dearmor extracts the binary SSHSIG blob from an armored block, tolerating
// the surrounding whitespace/newline variance seen across ssh-keygen and git
// output.
func dearmor(armored []byte) ([]byte, error) {
	s := strings.TrimSpace(string(armored))
	if !strings.HasPrefix(s, armorHeader) {
		return nil, errors.New("sshsig: missing BEGIN SSH SIGNATURE header")
	}
	if !strings.HasSuffix(s, armorFooter) {
		return nil, errors.New("sshsig: missing END SSH SIGNATURE footer")
	}
	body := strings.TrimSuffix(strings.TrimPrefix(s, armorHeader), armorFooter)
	body = strings.Join(strings.Fields(body), "")

	data, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("sshsig: invalid base64 in signature block: %w", err)
	}
	return data, nil
}
