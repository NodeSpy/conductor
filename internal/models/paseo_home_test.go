package models

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// homeRunner is a paseo fixture that answers --version, provider ls and
// provider models, recording the full argv of every call so a test can assert
// on the home selector.
func homeRunner(version string, provOut, modelOut string) (Runner, *[][]string) {
	var calls [][]string
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, args)
		rest := args
		if len(rest) >= 2 && rest[0] == "--home" {
			rest = rest[2:]
		}
		switch {
		case len(rest) >= 1 && rest[0] == "--version":
			if version == "" {
				return nil, errors.New("no version")
			}
			return []byte(version), nil
		case len(rest) >= 2 && rest[1] == "ls":
			return []byte(provOut), nil
		case len(rest) >= 2 && rest[1] == "models":
			return []byte(modelOut), nil
		}
		return nil, errors.New("unexpected call: " + strings.Join(args, " "))
	}, &calls
}

const oneProvider = `[{"provider":"claude","label":"Claude","status":"available","enabled":"Enabled"}]`
const oneModel = `[{"model":"Sonnet 5","id":"claude-sonnet-5"}]`

// The bug this whole change exists for: a runtime configuring `home:` had its
// MODELS enumerated against paseo's DEFAULT home while its AGENTS launched in
// the configured one. Every command discovery issues must carry the selector.
func TestPaseoListerTargetsTheConfiguredHome(t *testing.T) {
	run, calls := homeRunner("0.9.1", oneProvider, oneModel)
	l := &paseoLister{bin: "paseo", home: "/srv/paseo", run: run}
	got, err := l.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.IDs(), []string{"claude-sonnet-5"}) {
		t.Fatalf("roster = %v", got.IDs())
	}
	for _, c := range *calls {
		if len(c) >= 1 && c[0] == "--version" {
			continue // the probe itself needs no home
		}
		if len(c) < 2 || c[0] != "--home" || c[1] != "/srv/paseo" {
			t.Errorf("discovery call missing home selector: %v", c)
		}
	}
	if len(*calls) < 2 {
		t.Fatalf("expected a version probe and at least one query, got %v", *calls)
	}
}

// PRE-0.9 paseo has no --home flag; passing it breaks every command. The gate
// is load-bearing, not cosmetic.
func TestPaseoListerOmitsHomeOnPre09Paseo(t *testing.T) {
	run, calls := homeRunner("0.8.4", oneProvider, oneModel)
	l := &paseoLister{bin: "paseo", home: "/srv/paseo", run: run}
	if _, err := l.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range *calls {
		if slices.Contains(c, "--home") {
			t.Errorf("pre-0.9 paseo was passed --home: %v", c)
		}
	}
}

// No home configured → no selector, and no version probe to pay for.
func TestPaseoListerWithNoHomeDoesNotProbeVersion(t *testing.T) {
	run, calls := homeRunner("0.9.1", oneProvider, oneModel)
	l := &paseoLister{bin: "paseo", run: run}
	if _, err := l.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range *calls {
		if slices.Contains(c, "--home") || slices.Contains(c, "--version") {
			t.Errorf("unexpected call with no home configured: %v", c)
		}
	}
}

// An unknown version is treated as newest, so a --version hiccup never
// silently strips the home from a box that needs it.
func TestPaseoListerUnknownVersionStillSendsHome(t *testing.T) {
	run, calls := homeRunner("", oneProvider, oneModel)
	l := &paseoLister{bin: "paseo", home: "/srv/paseo", run: run}
	if _, err := l.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	var sawHome bool
	for _, c := range *calls {
		if slices.Contains(c, "--home") {
			sawHome = true
		}
	}
	if !sawHome {
		t.Error("unknown version dropped --home; should assume newest")
	}
}

// paseo answers "I cannot enumerate" with an error ENVELOPE where an array was
// expected. That is a no-discovery answer, not a corrupt response — and the
// reason must survive into the message, because "no runtime could enumerate"
// with the cause discarded is what made this bug invisible for a day.
func TestPaseoListerErrorEnvelopeIsNoDiscoveryAndKeepsTheReason(t *testing.T) {
	const envelope = `{"error":{"code":"DAEMON_NOT_RUNNING","message":"Cannot connect to daemon at home /home/u/.paseo"}}`
	l := &paseoLister{bin: "paseo", home: "/srv/paseo", run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if slices.Contains(args, "--version") {
			return []byte("0.9.1"), nil
		}
		return []byte(envelope), nil
	}}
	_, err := l.List(context.Background())
	if !errors.Is(err, ErrNoDiscovery) {
		t.Fatalf("want ErrNoDiscovery, got %v", err)
	}
	if !strings.Contains(err.Error(), "DAEMON_NOT_RUNNING") {
		t.Errorf("reason discarded: %v", err)
	}
	if !strings.Contains(err.Error(), "/srv/paseo") {
		t.Errorf("error does not name the home conductor asked: %v", err)
	}
}
