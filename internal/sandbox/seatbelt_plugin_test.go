package sandbox

import (
	"strings"
	"testing"
)

// seatbeltPluginProfile renders pure text (no sandbox-exec invocation), so
// this exercises it directly on every OS, not just darwin.

// TestSeatbeltPluginProfileDeniesByDefault proves the profile's base is
// deny-default, and never reads the package-manager trees a plugin's
// declaration did not ask for — a plugin reads the OS, not what is
// installed on it (seatbeltPluginBase's own doc comment).
func TestSeatbeltPluginProfileDeniesByDefault(t *testing.T) {
	got := seatbeltPluginProfile(&PluginProfile{})
	if !strings.Contains(got, "(deny default)") {
		t.Fatalf("profile must deny by default:\n%s", got)
	}
	for _, pkgTree := range []string{"/opt/homebrew", "/usr/local"} {
		if strings.Contains(got, pkgTree) {
			t.Fatalf("profile must never read the package-manager tree %q:\n%s", pkgTree, got)
		}
	}
}

// TestSeatbeltPluginProfileProcessExecOnlyDeclaredLiterals proves process
// execution is allow-listed to exactly the given Exec paths, as literals —
// no blanket "(allow process-exec)" the way the agent base has, and nothing
// not named is runnable.
func TestSeatbeltPluginProfileProcessExecOnlyDeclaredLiterals(t *testing.T) {
	empty := seatbeltPluginProfile(&PluginProfile{})
	if strings.Contains(empty, "(allow process-exec)") {
		t.Fatalf("an empty Exec must never render a blanket process-exec allow:\n%s", empty)
	}
	if strings.Contains(empty, "process-exec") {
		t.Fatalf("an empty Exec must render no process-exec clause at all:\n%s", empty)
	}

	got := seatbeltPluginProfile(&PluginProfile{Exec: []string{"/usr/bin/git", "/usr/bin/gh"}})
	if !strings.Contains(got, `(allow process-exec`) {
		t.Fatalf("declared commands must be exec-allowed:\n%s", got)
	}
	for _, bin := range []string{"/usr/bin/git", "/usr/bin/gh"} {
		if !strings.Contains(got, `(literal "`+bin+`")`) {
			t.Fatalf("Exec %q must appear as its own literal:\n%s", bin, got)
		}
	}
	// No bare, unqualified "(allow process-exec)" clause — every exec grant
	// names its literals.
	if strings.Contains(got, "(allow process-exec)") {
		t.Fatalf("process-exec must never be a blanket allow, even with Exec set:\n%s", got)
	}
	if strings.Contains(got, "/usr/bin/curl") {
		t.Fatalf("an undeclared command must not appear anywhere in the profile:\n%s", got)
	}
}

// TestSeatbeltPluginProfileReadIsSubpathOnly proves Read grants read-only
// access as a subpath, never write.
func TestSeatbeltPluginProfileReadIsSubpathOnly(t *testing.T) {
	got := seatbeltPluginProfile(&PluginProfile{Read: []string{"/acme/config"}})
	if !strings.Contains(got, `(allow file-read* (subpath "/acme/config"))`) {
		t.Fatalf("Read must be granted as a read-only subpath:\n%s", got)
	}
	if strings.Contains(got, `file-write* (subpath "/acme/config")`) {
		t.Fatalf("a Read path must never also be write-granted:\n%s", got)
	}
}

// TestSeatbeltPluginProfileWriteIsReadAndWrite proves Write grants both
// read and write on its subpath (a plugin that can write somewhere can
// always read back what it wrote).
func TestSeatbeltPluginProfileWriteIsReadAndWrite(t *testing.T) {
	got := seatbeltPluginProfile(&PluginProfile{Write: []string{"/acme/data"}})
	if !strings.Contains(got, `(allow file-read* file-write* (subpath "/acme/data"))`) {
		t.Fatalf("Write must grant both read and write on its subpath:\n%s", got)
	}
}

// TestSeatbeltPluginProfileNetworkDeniedThenNarrowlyAllowed proves the
// network is deny-default, then opened ONLY to the egress proxy port, the
// declared loopback destinations, and declared listen ports bind — nothing
// else.
func TestSeatbeltPluginProfileNetworkDeniedThenNarrowlyAllowed(t *testing.T) {
	none := seatbeltPluginProfile(&PluginProfile{})
	if !strings.Contains(none, "(deny network*)") {
		t.Fatalf("network must be deny-default:\n%s", none)
	}
	if strings.Contains(none, "network-outbound") || strings.Contains(none, "network-bind") {
		t.Fatalf("no network grants without a proxy, loopback port, or listen port:\n%s", none)
	}

	got := seatbeltPluginProfile(&PluginProfile{ProxyPort: 4100, LoopbackPorts: []int{9000}, ListenPorts: []int{8080}})
	if !strings.Contains(got, "(deny network*)") {
		t.Fatalf("deny network* must still be present:\n%s", got)
	}
	if !strings.Contains(got, `(allow network-outbound (remote ip "localhost:4100"))`) {
		t.Fatalf("the proxy port must be reachable:\n%s", got)
	}
	if !strings.Contains(got, `(allow network-outbound (remote ip "localhost:9000"))`) {
		t.Fatalf("a declared loopback destination must be reachable:\n%s", got)
	}
	if !strings.Contains(got, `(allow network-bind network-inbound (local ip "*:8080"))`) {
		t.Fatalf("a declared listen port must bind:\n%s", got)
	}
	// Deny-network comes before any allow, so later allows are the narrow
	// carve-outs, not the other way around.
	if strings.Index(got, "(deny network*)") > strings.Index(got, `localhost:4100`) {
		t.Fatalf("deny network* must precede the narrow allows:\n%s", got)
	}
}

// TestSeatbeltPluginProfileKeychainOnlyWhenDeclared proves the Keychain
// Mach services and ~/Library/Keychains are granted only when the plugin
// declares Keychain, never otherwise, and scoped to the given Home.
func TestSeatbeltPluginProfileKeychainOnlyWhenDeclared(t *testing.T) {
	without := seatbeltPluginProfile(&PluginProfile{Home: "/Users/acme"})
	if strings.Contains(without, "Keychains") {
		t.Fatalf("no Keychain path without Keychain: true:\n%s", without)
	}
	for _, m := range agentMachKeychain {
		if strings.Contains(without, m) {
			t.Fatalf("no Keychain mach service %q without Keychain: true:\n%s", m, without)
		}
	}

	with := seatbeltPluginProfile(&PluginProfile{Keychain: true, Home: "/Users/acme"})
	if !strings.Contains(with, `(allow file-read* file-write* (subpath "/Users/acme/Library/Keychains"))`) {
		t.Fatalf("Keychain: true must grant the login Keychain's directory:\n%s", with)
	}
	for _, m := range agentMachKeychain {
		if !strings.Contains(with, `(global-name "`+m+`")`) {
			t.Fatalf("Keychain: true must grant mach service %q:\n%s", m, with)
		}
	}

	// Keychain: true with no Home set grants the mach services but no path
	// (there is nothing to scope it to).
	noHome := seatbeltPluginProfile(&PluginProfile{Keychain: true})
	if strings.Contains(noHome, "Keychains") {
		t.Fatalf("Keychain: true with no Home must not render a Keychains path:\n%s", noHome)
	}
}

// A keyring item reads the Keychain: its files read-only, and none of the
// services that show the user a dialog. keychain: true keeps both.
func TestPluginProfileKeychainReadOnly(t *testing.T) {
	ro := seatbeltPluginProfile(&PluginProfile{KeychainRead: true, Home: "/Users/u"})
	if !strings.Contains(ro, `(allow file-read* (subpath "/Users/u/Library/Keychains"))`) || strings.Contains(ro, `file-write* (subpath "/Users/u/Library/Keychains")`) {
		t.Fatalf("read-only Keychain files:\n%s", ro)
	}
	if !strings.Contains(ro, `"com.apple.SecurityServer"`) || strings.Contains(ro, `"com.apple.security.agent"`) || strings.Contains(ro, `"com.apple.security.authhost"`) {
		t.Fatalf("read-only Keychain services:\n%s", ro)
	}
	rw := seatbeltPluginProfile(&PluginProfile{Keychain: true, KeychainRead: true, Home: "/Users/u"})
	if !strings.Contains(rw, `file-write* (subpath "/Users/u/Library/Keychains")`) || !strings.Contains(rw, `"com.apple.security.agent"`) {
		t.Fatalf("keychain: true:\n%s", rw)
	}
}
