package config

import (
	"strings"
	"testing"
)

// THE TWO CLASSIFIERS MUST AGREE.
//
// A pack source is judged twice: once by the RESOLVER, deciding whether to go
// fetch it, and once by pack_trust/plugin_trust, deciding whether it is
// allowed to. If the trust answer is "local, skip the allowlist" while the
// resolver's is "clone this", the allowlist is not a boundary — it is a
// suggestion with a hole in it shaped exactly like the disagreement.
//
// That happened. Trust stripped `git::` and re-classified with its own prefix
// list; the resolver force-gits ANY `git::` and accepts any scp-form
// `user@host:path`. So `git::attacker@shorthost:org/evil-payload` was judged
// local by trust and cloneable by the resolver — and a hostile pack's
// requires.packs.<alias>.source rode past pack_trust entirely.
//
// This table asserts, per source string:
//
//	governed-by-the-allowlist(SourceAllowed)  ==  fetched-remotely(parseSource)
//
// A new source shape that one side learns and the other does not fails here.
func TestTrustAndFetchClassifiersAgree(t *testing.T) {
	for _, tc := range []struct {
		src    string
		remote bool
		why    string
	}{
		{"git::attacker@shorthost:org/evil-payload", true,
			"THE BYPASS: force-git + scp form. The resolver clones it over SSH, so " +
				"the allowlist must govern it"},
		{"attacker@shorthost:org/evil-payload", true,
			"bare scp form — safeGitTransport accepts user@host:path"},
		{"git@github.com:someone/else", true, "the conventional scp form"},
		{"git::github.com/your-org/anything", true, "force-git over a shorthand"},
		{"github.com/acme/review-kit", true, "the github shorthand"},
		{"gitlab.com/team/repo", true, "a dotted host shorthand on another forge"},
		{"ssh://git@host/o/r", true, "an explicit scheme"},
		{"https://gitlab.com/x/y", true, "…and another"},
		{"git://host/o/r", true, "plaintext — the transport check refuses it LATER; " +
			"it is still a fetch, so trust must see it"},
		{"git::file:///tmp/repo//kit@v2", true,
			"git::file:// CLONES a repo on disk. A hostile parent pack can point it " +
				"anywhere, so it is a fetch and needs listing"},
		{"some-unrecognized-shape", true,
			"FAIL CLOSED: a shape neither side recognizes must end up MORE restricted, " +
				"never less"},

		// Unambiguously the operator's own disk.
		{"./local/path", false, "relative"},
		{"../sibling/pack", false, "relative, up"},
		{"/opt/packs/kit", false, "absolute"},
		{"~/packs/kit", false, "home"},
		{"file:///abs/pack", false,
			"file:// WITHOUT git:: is an absolute local path — the resolver strips " +
				"the prefix and reads the directory"},
	} {
		t.Run(tc.src, func(t *testing.T) {
			// What the RESOLVER does: git spec => it fetches remotely.
			spec, err := parseSource(tc.src, t.TempDir())
			fetchedRemotely := err == nil && spec.git
			if err != nil {
				// A source the resolver REFUSES (bad transport) is still one it
				// tried to fetch rather than read off disk — the refusal is the
				// transport allowlist, downstream of this classification.
				_, remote := RemoteSourceRef(tc.src)
				fetchedRemotely = remote
			}

			// What TRUST does: governed => an allowlist that matches nothing
			// denies it.
			nothing := &PackTrustConfig{Allow: []string{"github.com/nobody/nothing"}}
			governed := !nothing.SourceAllowed(tc.src)

			if fetchedRemotely != tc.remote {
				t.Errorf("resolver: fetchedRemotely=%v want %v — %s", fetchedRemotely, tc.remote, tc.why)
			}
			if governed != tc.remote {
				t.Errorf("trust: governed-by-allowlist=%v want %v — %s", governed, tc.remote, tc.why)
			}
			if governed != fetchedRemotely {
				t.Fatalf("THE CLASSIFIERS DISAGREE about %q: trust says governed=%v, "+
					"the resolver says fetched-remotely=%v. Whichever is wrong, the gap "+
					"between them is a pack_trust bypass. — %s",
					tc.src, governed, fetchedRemotely, tc.why)
			}
			// The plugin surface shares the classifier too.
			if got := !nothing.PluginSourceAllowed(tc.src); got != tc.remote {
				t.Errorf("plugin trust: governed=%v want %v", got, tc.remote)
			}
		})
	}
}

// The official repos keep their default allow, and the segment-anchored match
// is unchanged — routing through the resolver's classifier must not have moved
// either.
func TestSharedClassifierKeepsOfficialAndAnchoredMatching(t *testing.T) {
	set := PackTrustConfig{Allow: []string{"github.com/acme/*"}}
	for _, s := range []string{OfficialPacksSource, OfficialPacksSource + "/sub"} {
		if !set.SourceAllowed(s) {
			t.Errorf("the official packs repo must still need no entry: %q", s)
		}
	}
	if !set.PluginSourceAllowed(OfficialSource) {
		t.Error("the official plugin repo must still need no entry")
	}
	if set.SourceAllowed("github.com/acme-evil/pack") {
		t.Error("the segment-anchored match must still reject a continued org name")
	}
	if !set.SourceAllowed("github.com/acme/anything//sub@v1") {
		t.Error("…and still admit a repo under the org")
	}
}

// THE EXPLOIT, end to end. A third-party pack's requires.packs.<alias>.source
// is untrusted input — the parent pack author writes it, and conductor fetches
// it. With `git::` + an scp-form host it was judged local by the trust check
// and cloned over SSH by the resolver.
func TestNestedDependencySourceCannotEscapePackTrust(t *testing.T) {
	const evil = "git::attacker@shorthost:org/evil-payload"
	dir := t.TempDir()
	// A pack the operator DID list, whose dependency points somewhere else.
	writePackSource(t, dir, "src/legit", `
pack:
  name: legit
  version: "1.0.0"
  requires:
    conductor: ">=0.1"
    packs:
      dep: { source: `+evil+` }
workflows:
  flow:
    steps: [{ id: s, type: agent, prompt: p }]
`)
	_, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
pack_trust:
  allow: [github.com/legit-org/*]
packs:
  legit: { source: ./src/legit }
`))
	if err == nil {
		t.Fatal("a dependency source outside pack_trust must be refused — a hostile " +
			"parent pack chooses this string, and conductor clones it")
	}
	if !strings.Contains(err.Error(), evil) {
		t.Errorf("the refusal should name the offending source: %v", err)
	}
	// It must be a TRUST refusal, not a FETCH failure. Without this, the test
	// would pass even if trust failed OPEN: the clone of a bogus host errors
	// anyway and also names the source, so `err != nil` proves nothing about
	// the allowlist. Pin the trust-specific message so a fail-open regression
	// is caught here, not masked by the fetch dying downstream.
	if !strings.Contains(err.Error(), "not in pack_trust") {
		t.Errorf("must be refused BY TRUST (before any fetch), not by a fetch failure: %v", err)
	}
	// …and listing it is what makes it resolvable (it then fails at FETCH,
	// not at trust — which is the correct order).
	dir2 := t.TempDir()
	writePackSource(t, dir2, "src/legit", `
pack:
  name: legit
  version: "1.0.0"
  requires:
    conductor: ">=0.1"
    packs:
      dep: { source: `+evil+` }
workflows:
  flow:
    steps: [{ id: s, type: agent, prompt: p }]
`)
	_, err = resolveAndLoad(t, writeDoc(t, dir2, `
connectors:
  gh: { use: github, token: x }
pack_trust:
  allow: [github.com/legit-org/*, "attacker@shorthost:org/*"]
packs:
  legit: { source: ./src/legit }
`))
	if err != nil && strings.Contains(err.Error(), "not in pack_trust") {
		t.Errorf("an allow-listed source must pass the TRUST gate (it may still fail "+
			"to fetch, which is a different error): %v", err)
	}
}
