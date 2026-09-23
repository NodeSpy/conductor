package models

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/paseover"
)

// paseo discovery — NATIVE (docs/design/runtimes-models-packs.md §3.1).
//
// paseo owns its provider list and each provider's models; it reads the vendor
// SDKs' own enumerations and calls the vendors directly. So conductor asks
// paseo and does nothing else: no models.dev, no credential handling, no
// second opinion. This is the whole point of the per-runtime seam — paseo is
// authoritative for paseo, and is NOT consulted for anything else.
//
// Discovery MUST target the same daemon home dispatch launches into. paseo
// 0.9+ hosts several homes at once and `--home` picks one; a box configuring
// `runtimes.paseo.home:` runs its agents there and NOTHING at the default
// ~/.paseo. Asking the default home what models exist therefore answers about
// a daemon that is not running — an empty roster, every fleet matching
// nothing, and every dispatch degrading to a bare launch that paseo then
// rejects with MISSING_PROVIDER.

func init() {
	Register("paseo", func(rt Runtime, _ *Catalog) Lister {
		return &paseoLister{bin: binOr(rt.Bin, "paseo"), home: rt.Home, run: execRunner}
	})
}

type paseoLister struct {
	bin  string
	home string
	run  Runner
}

// paseoProvider is one entry of `paseo provider ls --json`.
type paseoProvider struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Status   string `json:"status"`
	Enabled  string `json:"enabled"`
}

// paseoModel is one entry of `paseo provider models <provider> --json`.
type paseoModel struct {
	Model       string `json:"model"`
	ID          string `json:"id"`
	Description string `json:"description"`
}

// paseoError is paseo's error envelope, which it emits INSTEAD of the expected
// array (and, for some failures, on a zero exit status). Parsing it is what
// turns "cannot enumerate, no idea why" into a message naming the actual
// problem — typically DAEMON_NOT_RUNNING against the wrong home.
type paseoError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// args prefixes the home selector when one is configured and this paseo
// understands it. PRE-0.9 paseo has no `--home` flag and rejects the whole
// command, so the version gate is load-bearing, not cosmetic.
func (l *paseoLister) args(ctx context.Context, rest ...string) []string {
	if l.home == "" || !l.version(ctx).HasHomeFlag() {
		return rest
	}
	return append([]string{"--home", l.home}, rest...)
}

func (l *paseoLister) version(ctx context.Context) paseover.Semver {
	out, err := l.run(ctx, l.bin, "--version")
	return paseover.Parse(string(out), err)
}

// homeLabel names the daemon home in diagnostics, so an operator reading
// "cannot enumerate" can tell which daemon conductor actually asked.
func (l *paseoLister) homeLabel() string {
	if l.home == "" {
		return "paseo's default home"
	}
	return "home " + l.home
}

// List asks paseo for every AVAILABLE provider's models, in paseo's own order
// (which is already newest-first per provider). A provider paseo reports as
// unavailable is skipped: its models cannot actually be launched, and a
// wildcard must never resolve to something the box cannot run.
func (l *paseoLister) List(ctx context.Context) (Roster, error) {
	out, err := l.run(ctx, l.bin, l.args(ctx, "provider", "ls", "--json")...)
	if err != nil {
		return nil, fmt.Errorf("%w: paseo provider ls (%s): %v", ErrNoDiscovery, l.homeLabel(), err)
	}
	var provs []paseoProvider
	if err := json.Unmarshal(out, &provs); err != nil {
		// An error envelope is paseo ANSWERING that it cannot enumerate, not a
		// corrupt response — report it as no-discovery (retriable) and name the
		// cause, rather than as a parse failure with the reason thrown away.
		if detail, ok := paseoErrorEnvelope(out); ok {
			return nil, fmt.Errorf("%w: paseo provider ls (%s): %s", ErrNoDiscovery, l.homeLabel(), detail)
		}
		return nil, fmt.Errorf("parse paseo provider ls (%s): %w", l.homeLabel(), err)
	}
	var roster Roster
	for _, p := range provs {
		if p.Provider == "" || p.Status != "available" {
			continue
		}
		mout, err := l.run(ctx, l.bin, l.args(ctx, "provider", "models", p.Provider, "--json")...)
		if err != nil {
			// One unreachable provider must not blank the whole roster.
			continue
		}
		var ms []paseoModel
		if err := json.Unmarshal(mout, &ms); err != nil {
			continue
		}
		for _, m := range ms {
			if m.ID == "" {
				continue
			}
			roster = append(roster, Model{ID: m.ID, Name: m.Model, Provider: p.Provider})
		}
	}
	if len(roster) == 0 {
		return nil, fmt.Errorf("%w: paseo reported no available provider models (%s)", ErrNoDiscovery, l.homeLabel())
	}
	return roster.Dedupe(), nil
}

// paseoErrorEnvelope extracts "CODE: message" from paseo's error envelope.
func paseoErrorEnvelope(out []byte) (string, bool) {
	var e paseoError
	if json.Unmarshal(out, &e) != nil || e.Error.Message == "" {
		return "", false
	}
	msg := strings.TrimSpace(e.Error.Message)
	if e.Error.Code != "" {
		return e.Error.Code + ": " + msg, true
	}
	return msg, true
}
