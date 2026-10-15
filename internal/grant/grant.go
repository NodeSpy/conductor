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
// # What "the same command" means
//
// Both sides parse argv with ParseArgv and compare with Argv.Equal. Exactly:
//
//   - Words — every token that is not a self-contained `-k=v` / `--k=v` flag,
//     INCLUDING a bare `-flag` and the token after it — must be equal, in
//     order. So reordering words, or a bare `-var-file a.tfvars` pair, is a
//     different command.
//   - `-k=v` flags are compared as a SET of names: their position among the
//     words and relative to each other does not matter (terraform reads them
//     that way too). Per name, the VALUES must be equal in order, so a
//     repeated flag's value order matters.
//   - Any extra, missing or changed token — word, flag name or flag value —
//     is a mismatch.
//
// So it is NOT true that "flag order can't change": `-a=1 -b=2` and
// `-b=2 -a=1` are the same command; `-a=1 -a=2` and `-a=2 -a=1` are not.
//
// # The working directory is part of the command
//
// `cd envs/prod && terraform apply tfplan` and the same argv run from
// envs/staging are different actions (a different root module, different
// state). A grant names its Cwd — workspace-relative, cleaned, never escaping
// the workspace ("." is the root) — and Matches requires the presented
// command's Cwd to be the same directory. A file the command names is pinned
// by its WORKSPACE-RELATIVE path: the argv token resolved against Cwd
// (ResolvePath), which is how the broker resolves it too.
//
// # Telling a broker refusal apart
//
// Find returns only live, matching grants. Near returns every grant for the
// presenting agent and tool regardless of argv or liveness, with State set,
// and Explain says why one of them does not apply (ErrMismatch, ErrExpired,
// ErrRevoked, ErrConsumed) — so a refusal can name its reason.
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
	"path"
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
	// Path is the file's WORKSPACE-RELATIVE path: the argv token resolved
	// against the grant's Cwd with ResolvePath ("envs/prod/tfplan" for
	// `tfplan` run in envs/prod). The broker resolves the token it sees the
	// same way and keys its digests by the result.
	Path string `json:"path"`
	// SHA256 is the lowercase hex SHA-256 of the approved content.
	SHA256 string `json:"sha256"`
}

// GrantSpec is one approved command. Every field except ID and Issued is set
// by the gate when it mints; the store fills those two.
type GrantSpec struct {
	// ID is the store-assigned handle (random, unguessable).
	ID string `json:"id"`
	// AgentID is the JAIL IDENTITY of the agent the grant is for: what the
	// jail keys the agent's tool state by and presents as Presented.AgentID.
	// For a paseo (ACP) session that is the identity the cwd→identity lookup
	// registered with the jail returned at session creation — for a member's
	// home session "member-<member>-<session>" — and NEVER paseo's own agent
	// id, which does not cross ACP. Dispatch is conductor's dispatch id (what
	// a cli launch is bound by). At least one is required; every one that is
	// set must match what the broker presents.
	AgentID  string `json:"agent_id,omitempty"`
	Dispatch string `json:"dispatch,omitempty"`
	// Tool is the binary's base name ("terraform").
	Tool string `json:"tool"`
	// Cwd is the directory the command runs in, workspace-relative and
	// cleaned ("." = the workspace root; see CleanCwd). It is part of the
	// command: the same argv from another directory does not match.
	Cwd string `json:"cwd"`
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
	// State is the grant's lifecycle state as the store last saw it — "live",
	// "used", "revoked" or "expired" — filled on the copies a store RETURNS
	// (Get, Near, Find, ByCase, Consume). Read-only: Mint ignores it.
	State string `json:"state,omitempty"`
}

// Grant states (GrantSpec.State).
const (
	StateLive    = "live"
	StateUsed    = "used"
	StateRevoked = "revoked"
	StateExpired = "expired"
)

// Explain reports why grant g does not apply to presented command p at time
// now: ErrRevoked, ErrConsumed or ErrExpired (from its State, and its Expiry),
// ErrMismatch when it is not this command for this agent, or nil when it
// applies. It reads only g and p — call it on Near's results.
func Explain(g GrantSpec, p Presented, now time.Time) error {
	switch {
	case g.State == StateRevoked:
		return ErrRevoked
	case g.State == StateUsed:
		return ErrConsumed
	case g.State == StateExpired || !now.Before(g.Expiry):
		return ErrExpired
	case !g.Matches(p):
		return ErrMismatch
	}
	return nil
}

// Presented is what the broker knows about a command it is deciding on.
// Cwd is the command's working directory relative to the workspace (any form
// CleanCwd accepts; one that escapes the workspace matches nothing).
type Presented struct {
	AgentID  string
	Dispatch string
	Tool     string
	Argv     Argv
	Cwd      string
}

// CleanCwd canonicalizes a workspace-relative directory: "" and "." are the
// root ("."), separators are "/", trailing slashes and "." segments drop. An
// absolute path, a backslash, a NUL, or anything that climbs out of the
// workspace ("..", "../x", "a/../../x") is an error.
func CleanCwd(dir string) (string, error) {
	if strings.ContainsAny(dir, "\\\x00") {
		return "", fmt.Errorf("%w: directory %q has a backslash or NUL", ErrInvalid, dir)
	}
	if strings.HasPrefix(dir, "/") {
		return "", fmt.Errorf("%w: directory %q must be workspace-relative, not absolute", ErrInvalid, dir)
	}
	if dir == "" {
		return ".", nil
	}
	c := path.Clean(dir)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("%w: directory %q escapes the workspace", ErrInvalid, dir)
	}
	return c, nil
}

// ResolvePath is the workspace-relative path of a file a command names: the
// argv token joined to the command's cwd and cleaned. An absolute token, or
// one that resolves outside the workspace, is an error. The gate pins files
// by this path and the broker resolves the token it sees the same way.
func ResolvePath(cwd, token string) (string, error) {
	c, err := CleanCwd(cwd)
	if err != nil {
		return "", err
	}
	if token == "" || strings.HasPrefix(token, "/") || strings.ContainsAny(token, "\\\x00") {
		return "", fmt.Errorf("%w: file %q must be a relative path", ErrInvalid, token)
	}
	p := path.Clean(path.Join(c, token))
	if p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return "", fmt.Errorf("%w: file %q (from %s) escapes the workspace", ErrInvalid, token, c)
	}
	return p, nil
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
	if c, err := CleanCwd(g.Cwd); err != nil {
		return err
	} else if g.Cwd != "" && c != g.Cwd {
		return bad("cwd %q is not clean (want %q)", g.Cwd, c)
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
		if c, err := CleanCwd(d.Path); err != nil || c != d.Path || c == "." {
			return bad("file %q must be a clean workspace-relative path (see ResolvePath)", d.Path)
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
	gc, err := CleanCwd(g.Cwd)
	if err != nil {
		return false
	}
	pc, err := CleanCwd(p.Cwd)
	if err != nil || pc != gc {
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
	// Near lists the grants for p's AGENT and TOOL — every binding that is set
	// on a grant must match p — regardless of argv match or liveness, with
	// State set, soonest expiry first. It is how the broker says WHY nothing
	// applied (Explain), where Find only answers whether something did.
	Near(ctx context.Context, p Presented) ([]GrantSpec, error)
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
