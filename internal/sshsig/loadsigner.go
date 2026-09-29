package sshsig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// errKeyNotInAgent means the agent was reachable but does not hold the
// requested key, as opposed to the agent being unreachable at all — callers
// that have a disk fallback use this to decide whether to try it.
var errKeyNotInAgent = errors.New("key not held by ssh-agent")

// LoadSigner resolves a git `user.signingkey` value to a signer, without
// ever returning or logging private key material. signingKey may be:
//
//   - "key::<openssh pubkey line>", or a bare "ssh-ed25519 AAAA..." line:
//     the key is looked up by public key in the ssh-agent at agentSock (or
//     $SSH_AUTH_SOCK when agentSock is empty). There is no disk fallback,
//     since a literal key gives us no path to a private key file.
//   - a path to a ".pub" file: an agent key matching that public key if the
//     agent has one, else the private key at the same path with ".pub"
//     stripped (must be unencrypted).
//   - a path to a private key file: loaded directly (must be unencrypted).
//
// A leading "~/" is expanded against the current user's home directory. An
// encrypted private key with no matching agent identity is a clear,
// key-material-free error telling the caller to load it into ssh-agent.
func LoadSigner(signingKey, agentSock string) (ssh.Signer, error) {
	key := strings.TrimSpace(signingKey)
	if key == "" {
		return nil, errors.New("sshsig: signing key is empty")
	}

	if literal, ok := strings.CutPrefix(key, "key::"); ok {
		return literalKeySigner(strings.TrimSpace(literal), agentSock)
	}
	if pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key)); err == nil {
		return agentSignerOrError(pub, agentSock)
	}

	path, err := expandHome(key)
	if err != nil {
		return nil, err
	}

	if strings.HasSuffix(path, ".pub") {
		return signerFromPubPath(path, agentSock)
	}
	return loadPrivateKeyFile(path)
}

// literalKeySigner handles the "key::<pubkey line>" form: the key only ever
// exists (from our point of view) inside the agent.
func literalKeySigner(literal string, agentSock string) (ssh.Signer, error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(literal))
	if err != nil {
		return nil, fmt.Errorf("sshsig: parse key:: public key: %w", err)
	}
	return agentSignerOrError(pub, agentSock)
}

// agentSignerOrError looks up pub in the agent and turns the "not found"
// case into a caller-facing error, for the two paths that have no disk
// fallback.
func agentSignerOrError(pub ssh.PublicKey, agentSock string) (ssh.Signer, error) {
	signer, err := findInAgent(pub, agentSock)
	if err != nil {
		return nil, fmt.Errorf("sshsig: %w", err)
	}
	return signer, nil
}

// signerFromPubPath implements the ".pub file" resolution rule: prefer the
// agent, fall back to the sibling private key file for any reason the agent
// lookup didn't pan out (not running, key absent, socket error, ...).
func signerFromPubPath(path, agentSock string) (ssh.Signer, error) {
	pubBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sshsig: read public key %s: %w", path, err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(pubBytes)
	if err != nil {
		return nil, fmt.Errorf("sshsig: parse public key %s: %w", path, err)
	}

	if signer, err := findInAgent(pub, agentSock); err == nil {
		return signer, nil
	}

	privPath := strings.TrimSuffix(path, ".pub")
	return loadPrivateKeyFile(privPath)
}

// loadPrivateKeyFile loads an unencrypted private key from disk. An
// encrypted key is reported without ever touching its passphrase or bytes.
func loadPrivateKeyFile(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sshsig: read private key %s: %w", path, err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		var passErr *ssh.PassphraseMissingError
		if errors.As(err, &passErr) {
			return nil, fmt.Errorf("sshsig: private key %s is encrypted; load it into ssh-agent instead (unattended signing cannot prompt for a passphrase)", path)
		}
		return nil, fmt.Errorf("sshsig: parse private key %s: %w", path, err)
	}
	return signer, nil
}

// expandHome expands a leading "~" or "~/..." against the current user's
// home directory; every other path is returned unchanged.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("sshsig: resolve home directory: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// findInAgent returns a signer for pub from the ssh-agent at agentSock (or
// $SSH_AUTH_SOCK), or errKeyNotInAgent-wrapping error if the agent doesn't
// hold it.
func findInAgent(pub ssh.PublicKey, agentSock string) (ssh.Signer, error) {
	sock := agentSock
	if sock == "" {
		sock = os.Getenv("SSH_AUTH_SOCK")
	}
	if sock == "" {
		return nil, fmt.Errorf("%w: SSH_AUTH_SOCK is not set", errKeyNotInAgent)
	}

	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("%w: connect to ssh-agent at %s: %v", errKeyNotInAgent, sock, err)
	}

	client := agent.NewClient(conn)
	signers, err := client.Signers()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: list ssh-agent identities: %v", errKeyNotInAgent, err)
	}

	want := pub.Marshal()
	for _, s := range signers {
		if bytes.Equal(s.PublicKey().Marshal(), want) {
			// The connection must stay open for the lifetime of this
			// signer (signing happens lazily, later); it is closed once
			// this signer has been used, since callers ask for a signer
			// to make exactly one signature.
			return &agentSigner{inner: s, conn: conn}, nil
		}
	}

	conn.Close()
	return nil, fmt.Errorf("%w: %s", errKeyNotInAgent, pub.Type())
}

// agentSigner adapts a live ssh-agent connection's signer to close its
// socket once it has been used, so a long-running process (a daemon calling
// LoadSigner repeatedly) doesn't accumulate open agent connections.
type agentSigner struct {
	inner ssh.Signer
	conn  net.Conn
}

func (a *agentSigner) PublicKey() ssh.PublicKey { return a.inner.PublicKey() }

func (a *agentSigner) Sign(rand io.Reader, data []byte) (*ssh.Signature, error) {
	defer a.conn.Close()
	return a.inner.Sign(rand, data)
}

// SignWithAlgorithm makes agentSigner an ssh.AlgorithmSigner whenever the
// underlying agent identity is one too, which is required for RSA keys to
// produce rsa-sha2-512 (rather than ssh-rsa/SHA-1) signatures.
func (a *agentSigner) SignWithAlgorithm(rand io.Reader, data []byte, algorithm string) (*ssh.Signature, error) {
	defer a.conn.Close()
	algSigner, ok := a.inner.(ssh.AlgorithmSigner)
	if !ok {
		return nil, fmt.Errorf("sshsig: agent key %s does not support algorithm %s", a.inner.PublicKey().Type(), algorithm)
	}
	return algSigner.SignWithAlgorithm(rand, data, algorithm)
}

var _ ssh.AlgorithmSigner = (*agentSigner)(nil)
