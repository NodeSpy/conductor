package sshsig

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// ---- pure-Go round trip ----------------------------------------------------

func TestSignVerifyRoundTrip(t *testing.T) {
	message := []byte("tree deadbeef\nauthor a <a@example.com> 0 +0000\n\ncommit message\n")

	for _, tc := range []struct {
		name string
		key  interface{}
	}{
		{"ed25519", genEd25519(t)},
		{"rsa2048", genRSA(t, 2048)},
		{"ecdsa-p256", genECDSA(t, elliptic.P256())},
		{"ecdsa-p384", genECDSA(t, elliptic.P384())},
		{"ecdsa-p521", genECDSA(t, elliptic.P521())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signer, err := ssh.NewSignerFromKey(tc.key)
			if err != nil {
				t.Fatalf("NewSignerFromKey: %v", err)
			}

			armored, err := Sign(signer, "git", message)
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			if !bytes.HasPrefix(armored, []byte(armorHeader)) {
				t.Fatalf("armored signature missing header: %q", armored[:min(40, len(armored))])
			}

			gotKey, err := Verify(armored, "git", message)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if !bytes.Equal(gotKey.Marshal(), signer.PublicKey().Marshal()) {
				t.Fatalf("Verify returned a different public key than the signer")
			}

			if tc.name == "rsa2048" {
				format := signatureFormat(t, armored)
				if format != ssh.KeyAlgoRSASHA512 {
					t.Fatalf("rsa signature format = %q, want %q (never SHA-1 ssh-rsa)", format, ssh.KeyAlgoRSASHA512)
				}
			}
		})
	}
}

// signatureFormat dearmors an SSHSIG blob and reads out the embedded
// signature's format string, to assert RSA never falls back to ssh-rsa/SHA-1.
func signatureFormat(t *testing.T, armored []byte) string {
	t.Helper()
	blob, err := dearmor(armored)
	if err != nil {
		t.Fatalf("dearmor: %v", err)
	}
	r := &wireReader{b: blob}
	if _, err := r.raw(len(magic)); err != nil {
		t.Fatalf("read magic: %v", err)
	}
	if _, err := r.uint32(); err != nil {
		t.Fatalf("read version: %v", err)
	}
	for i := 0; i < 4; i++ { // publickey, namespace, reserved, hash_algorithm
		if _, err := r.string(); err != nil {
			t.Fatalf("read field %d: %v", i, err)
		}
	}
	sigField, err := r.string()
	if err != nil {
		t.Fatalf("read signature field: %v", err)
	}
	sr := &wireReader{b: sigField}
	format, err := sr.string()
	if err != nil {
		t.Fatalf("read signature format: %v", err)
	}
	return string(format)
}

func TestVerifyRejectsTampering(t *testing.T) {
	message := []byte("hello, world")
	signer, err := ssh.NewSignerFromKey(genEd25519(t))
	if err != nil {
		t.Fatalf("NewSignerFromKey: %v", err)
	}
	armored, err := Sign(signer, "git", message)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	t.Run("wrong namespace", func(t *testing.T) {
		if _, err := Verify(armored, "file", message); err == nil {
			t.Fatal("Verify accepted a signature under the wrong namespace")
		}
	})

	t.Run("tampered message", func(t *testing.T) {
		if _, err := Verify(armored, "git", []byte("goodbye, world")); err == nil {
			t.Fatal("Verify accepted a signature over a different message")
		}
	})

	t.Run("tampered signature", func(t *testing.T) {
		blob, err := dearmor(armored)
		if err != nil {
			t.Fatalf("dearmor: %v", err)
		}
		tampered := append([]byte(nil), blob...)
		tampered[len(tampered)-1] ^= 0xff
		if _, err := Verify(armor(tampered), "git", message); err == nil {
			t.Fatal("Verify accepted a corrupted signature blob")
		}
	})

	t.Run("garbage input", func(t *testing.T) {
		if _, err := Verify([]byte("not a signature"), "git", message); err == nil {
			t.Fatal("Verify accepted non-armored garbage")
		}
	})
}

// ---- ssh-keygen interop -----------------------------------------------------

func TestSSHKeygenInterop(t *testing.T) {
	requireSSHKeygen(t)
	dir := t.TempDir()
	msgPath := filepath.Join(dir, "msg")
	message := []byte("interop payload\n")
	if err := os.WriteFile(msgPath, message, 0o600); err != nil {
		t.Fatalf("write message: %v", err)
	}

	for _, keyType := range []struct {
		name string
		args []string
	}{
		{"ed25519", []string{"-t", "ed25519"}},
		{"rsa", []string{"-t", "rsa", "-b", "2048"}},
		{"ecdsa-p256", []string{"-t", "ecdsa", "-b", "256"}},
	} {
		t.Run(keyType.name, func(t *testing.T) {
			keyPath := filepath.Join(dir, keyType.name)
			sshKeygenGenerate(t, keyPath, keyType.args)

			priv, err := os.ReadFile(keyPath)
			if err != nil {
				t.Fatalf("read private key: %v", err)
			}
			signer, err := ssh.ParsePrivateKey(priv)
			if err != nil {
				t.Fatalf("ParsePrivateKey: %v", err)
			}
			pubLine, err := os.ReadFile(keyPath + ".pub")
			if err != nil {
				t.Fatalf("read public key: %v", err)
			}

			t.Run("our signature verifies with ssh-keygen", func(t *testing.T) {
				armored, err := Sign(signer, "git", message)
				if err != nil {
					t.Fatalf("Sign: %v", err)
				}
				sigPath := filepath.Join(t.TempDir(), "sig")
				if err := os.WriteFile(sigPath, armored, 0o600); err != nil {
					t.Fatalf("write signature: %v", err)
				}
				allowed := filepath.Join(t.TempDir(), "allowed_signers")
				if err := os.WriteFile(allowed, []byte("signer@test "+string(pubLine)), 0o600); err != nil {
					t.Fatalf("write allowed_signers: %v", err)
				}

				cmd := exec.Command("ssh-keygen", "-Y", "verify",
					"-f", allowed, "-I", "signer@test", "-n", "git", "-s", sigPath)
				cmd.Stdin = bytes.NewReader(message)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("ssh-keygen -Y verify: %v: %s", err, out)
				}
			})

			t.Run("ssh-keygen signature verifies with us", func(t *testing.T) {
				sigPath := msgPath + ".sig"
				os.Remove(sigPath)
				cmd := exec.Command("ssh-keygen", "-Y", "sign", "-n", "git", "-f", keyPath, msgPath)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("ssh-keygen -Y sign: %v: %s", err, out)
				}
				armored, err := os.ReadFile(sigPath)
				if err != nil {
					t.Fatalf("read ssh-keygen signature: %v", err)
				}

				gotKey, err := Verify(armored, "git", message)
				if err != nil {
					t.Fatalf("Verify: %v", err)
				}
				wantKey, _, _, _, err := ssh.ParseAuthorizedKey(pubLine)
				if err != nil {
					t.Fatalf("ParseAuthorizedKey: %v", err)
				}
				if !bytes.Equal(gotKey.Marshal(), wantKey.Marshal()) {
					t.Fatal("Verify returned a different key than the one that signed")
				}
			})
		})
	}
}

// ---- LoadSigner -------------------------------------------------------------

func TestLoadSignerPubPathFallsBackToPrivateKey(t *testing.T) {
	dir := t.TempDir()
	privPath := filepath.Join(dir, "id_ed25519")
	pubPath := writeKeyPairFiles(t, privPath, genEd25519(t), "")

	noAgent := filepath.Join(dir, "no-such-agent.sock")
	signer, err := LoadSigner(pubPath, noAgent)
	if err != nil {
		t.Fatalf("LoadSigner: %v", err)
	}
	if err := roundTripWithSigner(t, signer); err != nil {
		t.Fatalf("round trip with loaded signer: %v", err)
	}
}

func TestLoadSignerPrivateKeyPath(t *testing.T) {
	dir := t.TempDir()
	privPath := filepath.Join(dir, "id_ed25519")
	writeKeyPairFiles(t, privPath, genEd25519(t), "")

	noAgent := filepath.Join(dir, "no-such-agent.sock")
	signer, err := LoadSigner(privPath, noAgent)
	if err != nil {
		t.Fatalf("LoadSigner: %v", err)
	}
	if err := roundTripWithSigner(t, signer); err != nil {
		t.Fatalf("round trip with loaded signer: %v", err)
	}
}

func TestLoadSignerLiteralKeyViaAgent(t *testing.T) {
	key := genEd25519(t)
	sockPath, cleanup := startTestAgent(t, key)
	defer cleanup()

	pub, err := ssh.NewPublicKey(key.Public())
	if err != nil {
		t.Fatalf("NewPublicKey: %v", err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))

	for _, form := range []string{line, "key::" + line} {
		signer, err := LoadSigner(form, sockPath)
		if err != nil {
			t.Fatalf("LoadSigner(%q): %v", form, err)
		}
		if !bytes.Equal(signer.PublicKey().Marshal(), pub.Marshal()) {
			t.Fatal("LoadSigner returned a signer for the wrong key")
		}
		if err := roundTripWithSigner(t, signer); err != nil {
			t.Fatalf("round trip with agent signer: %v", err)
		}
	}
}

func TestLoadSignerLiteralKeyNotInAgentErrors(t *testing.T) {
	// A key the (empty) agent has never seen.
	other := genEd25519(t)
	sockPath, cleanup := startTestAgent(t, genEd25519(t))
	defer cleanup()

	pub, err := ssh.NewPublicKey(other.Public())
	if err != nil {
		t.Fatalf("NewPublicKey: %v", err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))

	if _, err := LoadSigner(line, sockPath); err == nil {
		t.Fatal("LoadSigner found a key the agent doesn't have")
	}
}

func TestLoadSignerEncryptedKeyWithoutAgentErrors(t *testing.T) {
	dir := t.TempDir()
	key := genEd25519(t)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte("correct horse battery staple"))
	if err != nil {
		t.Fatalf("MarshalPrivateKeyWithPassphrase: %v", err)
	}
	privPath := filepath.Join(dir, "id_ed25519")
	privPEM := pem.EncodeToMemory(block)
	if err := os.WriteFile(privPath, privPEM, 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}

	noAgent := filepath.Join(dir, "no-such-agent.sock")
	_, err = LoadSigner(privPath, noAgent)
	if err == nil {
		t.Fatal("LoadSigner accepted an encrypted key with no agent")
	}
	if !strings.Contains(err.Error(), "ssh-agent") {
		t.Fatalf("error should point the caller at ssh-agent, got: %v", err)
	}
	// The error must never leak key material.
	if strings.Contains(err.Error(), "PRIVATE KEY") || bytes.Contains([]byte(err.Error()), privPEM) {
		t.Fatalf("error leaked private key material: %v", err)
	}
}

func TestLoadSignerExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	privPath := filepath.Join(home, ".ssh", "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(privPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeKeyPairFiles(t, privPath, genEd25519(t), "")

	noAgent := filepath.Join(home, "no-such-agent.sock")
	signer, err := LoadSigner("~/.ssh/id_ed25519", noAgent)
	if err != nil {
		t.Fatalf("LoadSigner: %v", err)
	}
	if err := roundTripWithSigner(t, signer); err != nil {
		t.Fatalf("round trip: %v", err)
	}
}

// ---- git end-to-end ---------------------------------------------------------

func TestGitEndToEnd(t *testing.T) {
	requireGit(t)
	requireSSHKeygen(t)

	repoDir := t.TempDir()
	keyDir := t.TempDir()
	keyPath := filepath.Join(keyDir, "id_ed25519")
	sshKeygenGenerate(t, keyPath, []string{"-t", "ed25519"})
	pubLine, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatalf("read public key: %v", err)
	}

	env := isolatedGitEnv(t)
	runGit(t, repoDir, env, "init", "-q")
	runGit(t, repoDir, env, "config", "user.name", "Test Signer")
	runGit(t, repoDir, env, "config", "user.email", "signer@test")
	runGit(t, repoDir, env, "config", "gpg.format", "ssh")
	runGit(t, repoDir, env, "config", "user.signingkey", keyPath)

	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, repoDir, env, "add", "file.txt")

	// --- direction 1: git signs, we verify -----------------------------
	if out, err := runGitOut(t, repoDir, env, "commit", "-q", "-S", "-m", "signed commit"); err != nil {
		t.Skipf("git could not produce an ssh-signed commit (likely git version): %v: %s", err, out)
	}
	raw := runGitOut2(t, repoDir, env, "cat-file", "commit", "HEAD")

	headers, sigLines, message := splitCommitGpgsig(raw)
	if len(sigLines) == 0 {
		t.Fatalf("commit has no gpgsig header:\n%s", raw)
	}
	armored := strings.Join(sigLines, "\n") + "\n"
	payload := strings.Join(headers, "\n") + "\n\n" + message

	wantKey, _, _, _, err := ssh.ParseAuthorizedKey(pubLine)
	if err != nil {
		t.Fatalf("ParseAuthorizedKey: %v", err)
	}
	gotKey, err := Verify([]byte(armored), "git", []byte(payload))
	if err != nil {
		t.Fatalf("Verify(git commit signature): %v", err)
	}
	if !bytes.Equal(gotKey.Marshal(), wantKey.Marshal()) {
		t.Fatal("Verify returned a different key than the committer's")
	}

	// --- direction 2: we sign, git verifies -----------------------------
	if err := os.WriteFile(filepath.Join(repoDir, "file2.txt"), []byte("world\n"), 0o600); err != nil {
		t.Fatalf("write file2: %v", err)
	}
	runGit(t, repoDir, env, "add", "file2.txt")
	runGit(t, repoDir, env, "commit", "-q", "-m", "unsigned commit")
	unsignedRaw := runGitOut2(t, repoDir, env, "cat-file", "commit", "HEAD")

	priv, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read private key: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(priv)
	if err != nil {
		t.Fatalf("ParsePrivateKey: %v", err)
	}
	ourSig, err := Sign(signer, "git", unsignedRaw)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	unsignedHeaders, _, unsignedMessage := splitCommitGpgsig(unsignedRaw)
	newRaw := strings.Join(unsignedHeaders, "\n") + "\n" + foldGitHeader("gpgsig", ourSig) + "\n\n" + unsignedMessage

	hashCmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	hashCmd.Dir = repoDir
	hashCmd.Env = env
	hashCmd.Stdin = strings.NewReader(newRaw)
	shaOut, err := hashCmd.Output()
	if err != nil {
		t.Fatalf("git hash-object: %v", err)
	}
	sha := strings.TrimSpace(string(shaOut))

	allowed := filepath.Join(keyDir, "allowed_signers")
	if err := os.WriteFile(allowed, []byte("signer@test "+string(pubLine)), 0o600); err != nil {
		t.Fatalf("write allowed_signers: %v", err)
	}
	runGit(t, repoDir, env, "config", "gpg.ssh.allowedSignersFile", allowed)

	if out, err := runGitOut(t, repoDir, env, "verify-commit", sha); err != nil {
		t.Fatalf("git verify-commit rejected our signature: %v: %s", err, out)
	}
}

// splitCommitGpgsig splits the raw text of `git cat-file commit <sha>` into
// the non-gpgsig header lines, the folded gpgsig value's lines (with the
// per-line leading space stripped), and the commit message.
func splitCommitGpgsig(raw []byte) (headers []string, gpgsig []string, message string) {
	parts := strings.SplitN(string(raw), "\n\n", 2)
	if len(parts) == 2 {
		message = parts[1]
	}
	inSig := false
	for _, l := range strings.Split(parts[0], "\n") {
		switch {
		case strings.HasPrefix(l, "gpgsig "):
			inSig = true
			gpgsig = append(gpgsig, strings.TrimPrefix(l, "gpgsig "))
		case inSig && strings.HasPrefix(l, " "):
			gpgsig = append(gpgsig, l[1:])
		default:
			inSig = false
			headers = append(headers, l)
		}
	}
	return headers, gpgsig, message
}

// foldGitHeader folds a multi-line header value (an armored SSHSIG block)
// into git's commit-object header continuation format: the first line stays
// on the "key value" line, every subsequent line gets a single leading
// space.
func foldGitHeader(key string, value []byte) string {
	lines := strings.Split(strings.TrimRight(string(value), "\n"), "\n")
	var b strings.Builder
	b.WriteString(key)
	b.WriteByte(' ')
	b.WriteString(lines[0])
	for _, l := range lines[1:] {
		b.WriteByte('\n')
		b.WriteByte(' ')
		b.WriteString(l)
	}
	return b.String()
}

// ---- test helpers -----------------------------------------------------------

func requireSSHKeygen(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func sshKeygenGenerate(t *testing.T, path string, typeArgs []string) {
	t.Helper()
	args := append([]string{}, typeArgs...)
	args = append(args, "-N", "", "-f", path, "-C", "sshsig-test")
	cmd := exec.Command("ssh-keygen", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func genEd25519(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return priv
}

func genRSA(t *testing.T, bits int) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return key
}

func genECDSA(t *testing.T, curve elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	return key
}

// writeKeyPairFiles writes an unencrypted OpenSSH private key and its
// matching ".pub" file for key, returning the public key path.
func writeKeyPairFiles(t *testing.T, privPath string, key interface{}, comment string) string {
	t.Helper()
	block, err := ssh.MarshalPrivateKey(key, comment)
	if err != nil {
		t.Fatalf("MarshalPrivateKey: %v", err)
	}
	if err := os.WriteFile(privPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("NewSignerFromKey: %v", err)
	}
	pubPath := privPath + ".pub"
	if err := os.WriteFile(pubPath, ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o644); err != nil {
		t.Fatalf("write public key: %v", err)
	}
	return pubPath
}

func roundTripWithSigner(t *testing.T, signer ssh.Signer) error {
	t.Helper()
	message := []byte("round trip via LoadSigner")
	armored, err := Sign(signer, "git", message)
	if err != nil {
		return fmt.Errorf("Sign: %w", err)
	}
	got, err := Verify(armored, "git", message)
	if err != nil {
		return fmt.Errorf("Verify: %w", err)
	}
	if !bytes.Equal(got.Marshal(), signer.PublicKey().Marshal()) {
		return errors.New("verified key does not match signer")
	}
	return nil
}

// startTestAgent serves an in-process agent.Keyring holding key over a unix
// socket in a short temp dir (a unix socket path is capped at 104 bytes on
// macOS, and t.TempDir() there is a long /var/folders/… path), returning the
// socket path and a cleanup func.
func startTestAgent(t *testing.T, key ed25519.PrivateKey) (string, func()) {
	t.Helper()
	sockDir, err := os.MkdirTemp("/tmp", "ssa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "agent.sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
		t.Fatalf("agent.Add: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go agent.ServeAgent(keyring, conn)
		}
	}()

	cleanup := func() {
		l.Close()
		<-done
	}
	return sockPath, cleanup
}

func isolatedGitEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	globalConfig := filepath.Join(home, ".gitconfig")
	if err := os.WriteFile(globalConfig, nil, 0o600); err != nil {
		t.Fatalf("write global gitconfig: %v", err)
	}

	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "HOME="),
			strings.HasPrefix(kv, "GIT_CONFIG_GLOBAL="),
			strings.HasPrefix(kv, "GIT_CONFIG_NOSYSTEM="),
			strings.HasPrefix(kv, "GIT_CONFIG_SYSTEM="),
			strings.HasPrefix(kv, "SSH_AUTH_SOCK="),
			strings.HasPrefix(kv, "GIT_AUTHOR_"),
			strings.HasPrefix(kv, "GIT_COMMITTER_"):
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"HOME="+home,
		"GIT_CONFIG_GLOBAL="+globalConfig,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	return env
}

func runGit(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	if out, err := runGitOut(t, dir, env, args...); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func runGitOut(t *testing.T, dir string, env []string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd.CombinedOutput()
}

// runGitOut2 is like runGitOut but fails the test on error and returns only
// stdout (used for `cat-file`, where stderr must stay out of the payload).
func runGitOut2(t *testing.T, dir string, env []string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}
