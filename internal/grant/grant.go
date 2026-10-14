// Package grant is the contract between conductor's approval gate and the
// agent jail's host-command broker.
//
// When an operator approves an infra action a member proposed — a
// `terraform apply <planfile>`, say — the gate MINTS a grant: one exact
// command (tool + parsed argv), bound to the agent that may run it, pinned to
// the content of the files it names, short-lived and usually single-use. The
// jail's broker, which sees every credentialed host command an agent runs,
// looks for a live grant matching the command it is about to refuse, verifies
// the file digests itself at use time (copying the file into its own private
// scratch first, so the bytes it checks are the bytes it runs), and CONSUMES
// the grant. Nothing else lifts a broker refusal, and nothing in this package
// runs a command.
//
// The spec deliberately carries no scratch path: where the broker stages the
// file is the broker's business, and a path the gate chose would be a path an
// agent could race.
//
// This package has no dependencies inside conductor, so both sides can import
// it without a cycle.
package grant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
)

// MaxTTL bounds how long any grant may live. An approval is for "now", not a
// standing permission: a grant that outlives the operator's attention is a
// grant nobody is watching.
const MaxTTL = 24 * time.Hour

// Argv is a command's arguments in parsed form: positional words and a
// normalized flag map — the same shape the jail's command parser produces
// (hostcmd.Parsed's Words/Flags), so matching is independent of flag order
// and of `--x=y` versus `--x y` spelling.
type Argv struct {
	// Words are the positional arguments after the tool name, in order
	// (["apply", "plan.tfplan"]).
	Words []string `json:"words"`
	// Flags maps a flag's long name ("--var-file") to its values in order of
	// appearance; a boolean flag records one empty value. Nil and empty are
	// equivalent.
	Flags map[string][]string `json:"flags,omitempty"`
}

// Equal reports whether two parsed argvs are the same command: the same words
// in the same order and the same flags with the same values. Flag ORDER does
// not matter (the map has none); value order within one flag does.
func (a Argv) Equal(b Argv) bool {
	if !slices.Equal(a.Words, b.Words) {
		return false
	}
	if len(a.Flags) != len(b.Flags) {
		return false
	}
	for k, v := range a.Flags {
		w, ok := b.Flags[k]
		if !ok || !slices.Equal(v, w) {
			return false
		}
	}
	return true
}

// String renders the argv for audit and for showing an operator what they
// are approving: words, then flags in sorted order.
func (a Argv) String() string {
	parts := append([]string(nil), a.Words...)
	keys := make([]string, 0, len(a.Flags))
	for k := range a.Flags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range a.Flags[k] {
			if v == "" {
				parts = append(parts, k)
			} else {
				parts = append(parts, k+"="+v)
			}
		}
	}
	return strings.Join(parts, " ")
}

// FileDigest pins one file the command names to the content the operator
// approved.
type FileDigest struct {
	// Path is the file as the command names it — the exact argv token
	// ("plan.tfplan"), resolved by the broker against the command's cwd.
	Path string `json:"path"`
	// SHA256 is the lowercase hex SHA-256 of the approved content.
	SHA256 string `json:"sha256"`
}

// GrantSpec is one approved command. Every field except ID and Issued is set
// by the gate when it mints; the store fills those two.
type GrantSpec struct {
	// ID is the store-assigned handle (random, unguessable).
	ID string `json:"id"`
	// AgentID is the agent the grant is for — the identity the jail resolves
	// from a command's cwd. Dispatch is conductor's dispatch/session id for
	// the same agent. At least one is required; every one that is set must
	// match what the broker presents.
	AgentID  string `json:"agent_id,omitempty"`
	Dispatch string `json:"dispatch,omitempty"`
	// Tool is the binary's base name ("terraform").
	Tool string `json:"tool"`
	// Argv is the exact command approved.
	Argv Argv `json:"argv"`
	// FileDigests pins each file the command reads that the approval was
	// about. The broker must check every one at use time; a grant whose
	// files changed since approval is not this grant.
	FileDigests []FileDigest `json:"file_digests,omitempty"`
	// Expiry is when the grant stops matching. Required, and at most MaxTTL
	// after Issued.
	Expiry time.Time `json:"expiry"`
	// SingleUse grants are consumed by their first successful use.
	SingleUse bool `json:"single_use"`
	// ApprovedBy names the operator who approved, as the gate authenticated
	// them. Required: a grant nobody approved is not a grant.
	ApprovedBy string `json:"approved_by"`
	// CaseID is the member case (ledger entry) the approval belongs to, so a
	// use can be reported back to it.
	CaseID string `json:"case_id,omitempty"`
	// Issued is when the store accepted the grant.
	Issued time.Time `json:"issued"`
}

// Presented is what the broker knows about a command it is deciding on.
type Presented struct {
	AgentID  string
	Dispatch string
	Tool     string
	Argv     Argv
}

// Errors. Use errors.Is: stores may wrap them with detail.
var (
	ErrInvalid  = errors.New("grant: invalid spec")
	ErrNotFound = errors.New("grant: not found")
	ErrExpired  = errors.New("grant: expired")
	ErrConsumed = errors.New("grant: already used")
	ErrRevoked  = errors.New("grant: revoked")
	ErrMismatch = errors.New("grant: does not match the presented command")
	ErrDigest   = errors.New("grant: file content does not match the approved digest")
)

// Validate checks a spec the gate is about to mint, at time now.
func (g GrantSpec) Validate(now time.Time) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
	}
	if strings.TrimSpace(g.AgentID) == "" && strings.TrimSpace(g.Dispatch) == "" {
		return bad("agent_id or dispatch is required")
	}
	if strings.TrimSpace(g.Tool) == "" || strings.ContainsAny(g.Tool, "/\\") {
		return bad("tool must be a bare binary name, got %q", g.Tool)
	}
	if len(g.Argv.Words) == 0 && len(g.Argv.Flags) == 0 {
		return bad("argv is empty — a grant names one exact command")
	}
	if strings.TrimSpace(g.ApprovedBy) == "" {
		return bad("approved_by is required")
	}
	if g.Expiry.IsZero() {
		return bad("expiry is required")
	}
	if !g.Expiry.After(now) {
		return bad("expiry %s is not in the future", g.Expiry.Format(time.RFC3339))
	}
	if g.Expiry.Sub(now) > MaxTTL {
		return bad("expiry is more than %s away", MaxTTL)
	}
	seen := map[string]bool{}
	for _, d := range g.FileDigests {
		if strings.TrimSpace(d.Path) == "" {
			return bad("file digest with an empty path")
		}
		if seen[d.Path] {
			return bad("file %q pinned twice", d.Path)
		}
		seen[d.Path] = true
		if !validHexSHA256(d.SHA256) {
			return bad("file %q: sha256 must be 64 lowercase hex chars", d.Path)
		}
	}
	return nil
}

// Matches reports whether a presented command is the one this grant names,
// for the identity it was issued to. It checks identity, tool and argv only —
// not expiry, use, or file content.
func (g GrantSpec) Matches(p Presented) bool {
	if g.AgentID != "" && g.AgentID != p.AgentID {
		return false
	}
	if g.Dispatch != "" && g.Dispatch != p.Dispatch {
		return false
	}
	return g.Tool == p.Tool && g.Argv.Equal(p.Argv)
}

// CheckDigests compares the digests the broker computed (argv path → hex
// SHA-256, over the bytes it is about to run) with the pinned ones. Every
// pinned file must be present and equal; extra entries are ignored.
func (g GrantSpec) CheckDigests(actual map[string]string) error {
	for _, d := range g.FileDigests {
		got, ok := actual[d.Path]
		if !ok {
			return fmt.Errorf("%w: %s was not checked", ErrDigest, d.Path)
		}
		if got != d.SHA256 {
			return fmt.Errorf("%w: %s", ErrDigest, d.Path)
		}
	}
	return nil
}

// FileSHA256 hashes a file, for the gate pinning a file at approval and for
// the broker checking its private copy at use.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Store holds minted grants. Implementations must make Consume atomic: two
// concurrent uses of a single-use grant succeed at most once.
type Store interface {
	// Mint validates g, assigns ID and Issued, and stores it.
	Mint(ctx context.Context, g GrantSpec) (GrantSpec, error)
	// Get returns a grant by id, live or not (ErrNotFound if unknown).
	Get(ctx context.Context, id string) (GrantSpec, error)
	// Find returns the live grants (unexpired, unused or multi-use, not
	// revoked) that match p, soonest-expiring first.
	Find(ctx context.Context, p Presented) ([]GrantSpec, error)
	// Consume is the use: it re-checks that grant id matches p and is live,
	// and marks a single-use grant used. It returns ErrExpired, ErrConsumed,
	// ErrRevoked, ErrMismatch or ErrNotFound when it does not hold. The
	// broker checks file digests BEFORE consuming (a digest mismatch must not
	// burn the operator's approval).
	Consume(ctx context.Context, id string, p Presented) (GrantSpec, error)
	// Revoke kills a grant (ErrNotFound if unknown). Idempotent.
	Revoke(ctx context.Context, id string) error
	// ByCase lists every grant minted for a case, live or not.
	ByCase(ctx context.Context, caseID string) ([]GrantSpec, error)
}

// NewID returns a random grant id.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("grant: crypto/rand: " + err.Error())
	}
	return "g_" + hex.EncodeToString(b[:])
}

func validHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ParseArgv is THE argv parse both sides of a grant use, so the gate and the
// broker can never disagree about what a command is. It is deliberately
// profile-free and conservative: only self-contained `-k=v` / `--k=v` tokens
// become Flags; every other token — including a bare `-flag` and whatever
// follows it — stays in Words, IN ORDER. So `-var-file a b` and `b -var-file a`
// are different commands (they are, to the tool), where a parse that lifted
// `-var-file` into the map would have made them equal.
func ParseArgv(args []string) Argv {
	a := Argv{Words: []string{}}
	for _, t := range args {
		if strings.HasPrefix(t, "-") && t != "-" && t != "--" {
			if k, v, ok := strings.Cut(t, "="); ok && strings.TrimLeft(k, "-") != "" {
				if a.Flags == nil {
					a.Flags = map[string][]string{}
				}
				a.Flags[k] = append(a.Flags[k], v)
				continue
			}
		}
		a.Words = append(a.Words, t)
	}
	return a
}
