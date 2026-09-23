package dispatch

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestParsePaseoSemver(t *testing.T) {
	cases := []struct {
		name          string
		out           string
		err           error
		wantKnown     bool
		maj, min, pat int
	}{
		{"plain", "0.9.1\n", nil, true, 0, 9, 1},
		{"no trailing newline", "0.9.1", nil, true, 0, 9, 1},
		{"prerelease", "0.9.0-beta.2\n", nil, true, 0, 9, 0},
		{"prefixed", "paseo 0.8.0\n", nil, true, 0, 8, 0},
		{"two-part", "1.2\n", nil, true, 1, 2, 0},
		{"command failed", "", errors.New("exec: not found"), false, 0, 0, 0},
		{"garbage", "not a version", nil, false, 0, 0, 0},
		{"empty", "", nil, false, 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := parsePaseoSemver(c.out, c.err)
			if v.Known != c.wantKnown {
				t.Fatalf("known=%v, want %v (%q)", v.Known, c.wantKnown, v)
			}
			if c.wantKnown && (v.Major != c.maj || v.Minor != c.min || v.Patch != c.pat) {
				t.Fatalf("got %d.%d.%d, want %d.%d.%d", v.Major, v.Minor, v.Patch, c.maj, c.min, c.pat)
			}
		})
	}
}

func TestPaseoSemverAtLeast(t *testing.T) {
	v091 := paseoSemver{Major: 0, Minor: 9, Patch: 1, Known: true}
	if !v091.AtLeast(0, 9, 0) {
		t.Error("0.9.1 should be >= 0.9.0")
	}
	if !v091.AtLeast(0, 9, 1) {
		t.Error("0.9.1 should be >= 0.9.1")
	}
	if v091.AtLeast(0, 9, 2) {
		t.Error("0.9.1 should NOT be >= 0.9.2")
	}
	v080 := paseoSemver{Major: 0, Minor: 8, Patch: 0, Known: true}
	if v080.AtLeast(0, 9, 0) {
		t.Error("0.8.0 should NOT be >= 0.9.0")
	}
	if !v080.AtLeast(0, 8, 0) {
		t.Error("0.8.0 should be >= 0.8.0")
	}
	v100 := paseoSemver{Major: 1, Minor: 0, Patch: 0, Known: true}
	if !v100.AtLeast(0, 9, 0) {
		t.Error("1.0.0 should be >= 0.9.0")
	}
	// Unknown → assume newest.
	if !(paseoSemver{}).AtLeast(0, 9, 0) {
		t.Error("unknown version should be treated as newest (>= 0.9.0)")
	}
}

// primeCache marks a cache as already-detected with a fixed version, so
// homePrefix consults it without shelling out to real paseo.
func primeCache(v paseoSemver) *paseoVersionCache {
	c := &paseoVersionCache{}
	c.once.Do(func() { c.ver = v })
	return c
}

func TestHomePrefixVersionGated(t *testing.T) {
	ctx := context.Background()

	// paseo >= 0.9 with a home → emit --home.
	got := homePrefix(ctx, "paseo", nil, "/srv/paseo", primeCache(paseoSemver{Major: 0, Minor: 9, Patch: 1, Known: true}), nil)
	if !slices.Equal(got, []string{"--home", "/srv/paseo"}) {
		t.Fatalf("0.9 with home: got %v, want [--home /srv/paseo]", got)
	}

	// pre-0.9 with a home → NO --home (the flag doesn't exist there).
	if got := homePrefix(ctx, "paseo", nil, "/srv/paseo", primeCache(paseoSemver{Major: 0, Minor: 8, Patch: 0, Known: true}), nil); got != nil {
		t.Fatalf("0.8 with home: got %v, want nil", got)
	}

	// No home configured → nil regardless of version.
	if got := homePrefix(ctx, "paseo", nil, "", primeCache(paseoSemver{Major: 0, Minor: 9, Patch: 1, Known: true}), nil); got != nil {
		t.Fatalf("empty home: got %v, want nil", got)
	}

	// Unknown version WITH a home → assume newest, emit --home (a home is only
	// ever configured on 0.9+, so this never regresses a real pre-0.9 box).
	if got := homePrefix(ctx, "paseo", nil, "/srv/paseo", primeCache(paseoSemver{}), nil); !slices.Equal(got, []string{"--home", "/srv/paseo"}) {
		t.Fatalf("unknown with home: got %v, want [--home /srv/paseo]", got)
	}
}

// TestDispatcherPaseoCmdInjectsHome exercises the real exec seam: the built argv
// carries --home only when the dispatcher has a home AND the (primed) version is
// >= 0.9. This is what every clone/run/ls/… actually gets.
func TestDispatcherPaseoCmdInjectsHome(t *testing.T) {
	d := &Dispatcher{PaseoBin: "paseo", Home: "/srv/paseo"}
	d.verCache.once.Do(func() { d.verCache.ver = paseoSemver{Major: 0, Minor: 9, Patch: 1, Known: true} })
	if got := d.paseoCmd(context.Background(), "clone", "acme/x").Args; !slices.Equal(got, []string{"paseo", "--home", "/srv/paseo", "clone", "acme/x"}) {
		t.Fatalf("0.9: got %v", got)
	}

	d8 := &Dispatcher{PaseoBin: "paseo", Home: "/srv/paseo"}
	d8.verCache.once.Do(func() { d8.verCache.ver = paseoSemver{Major: 0, Minor: 8, Patch: 0, Known: true} })
	if got := d8.paseoCmd(context.Background(), "clone", "acme/x").Args; !slices.Equal(got, []string{"paseo", "clone", "acme/x"}) {
		t.Fatalf("0.8: got %v (must NOT carry --home)", got)
	}

	dNoHome := &Dispatcher{PaseoBin: "paseo"}
	dNoHome.verCache.once.Do(func() { dNoHome.verCache.ver = paseoSemver{Major: 0, Minor: 9, Patch: 1, Known: true} })
	if got := dNoHome.paseoCmd(context.Background(), "ls", "--json").Args; !slices.Equal(got, []string{"paseo", "ls", "--json"}) {
		t.Fatalf("no home: got %v", got)
	}
}
