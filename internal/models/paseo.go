package models

import (
	"context"
	"encoding/json"
	"fmt"
)

// paseo discovery — NATIVE (docs/design/runtimes-models-packs.md §3.1).
//
// paseo owns its provider list and each provider's models; it reads the vendor
// SDKs' own enumerations and calls the vendors directly. So conductor asks
// paseo and does nothing else: no models.dev, no credential handling, no
// second opinion. This is the whole point of the per-runtime seam — paseo is
// authoritative for paseo, and is NOT consulted for anything else.

func init() {
	Register("paseo", func(rt Runtime, _ *Catalog) Lister {
		return &paseoLister{bin: binOr(rt.Bin, "paseo"), run: execRunner}
	})
}

type paseoLister struct {
	bin string
	run Runner
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

// List asks paseo for every AVAILABLE provider's models, in paseo's own order
// (which is already newest-first per provider). A provider paseo reports as
// unavailable is skipped: its models cannot actually be launched, and a
// wildcard must never resolve to something the box cannot run.
func (l *paseoLister) List(ctx context.Context) (Roster, error) {
	out, err := l.run(ctx, l.bin, "provider", "ls", "--json")
	if err != nil {
		return nil, fmt.Errorf("%w: paseo provider ls: %v", ErrNoDiscovery, err)
	}
	var provs []paseoProvider
	if err := json.Unmarshal(out, &provs); err != nil {
		return nil, fmt.Errorf("parse paseo provider ls: %w", err)
	}
	var roster Roster
	for _, p := range provs {
		if p.Provider == "" || p.Status != "available" {
			continue
		}
		mout, err := l.run(ctx, l.bin, "provider", "models", p.Provider, "--json")
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
		return nil, fmt.Errorf("%w: paseo reported no available provider models", ErrNoDiscovery)
	}
	return roster.Dedupe(), nil
}
