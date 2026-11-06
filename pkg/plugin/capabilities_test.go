package plugin

import (
	"reflect"
	"strings"
	"testing"
)

func TestCapabilitiesResolve(t *testing.T) {
	caps := Capabilities{
		Egress:   []string{"api.example.com:443", "*.cdn.example.com:443", "bare.example.com", "config:url", "config:relay.addr:8443"},
		Listen:   []string{"config:hook.listen", "127.0.0.1:9000"},
		Read:     []string{"~/.config/tool", "env:TOOL_DIR", "env:TOOL_DIR/sub", "config:key_path", "/etc/tool.conf"},
		Write:    []string{"config:state_dir"},
		FS:       []string{"/var/lib/tool"},
		Commands: []string{"tool"},
		Env:      []string{"TOOL_DIR", "TOOL_TOKEN"},
	}
	conn := Schema{"url": {}, "relay": {}, "hook": {}, "key_path": {}, "state_dir": {}}
	if err := caps.Validate(conn); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"TOOL_DIR": "/opt/tool"}
	g, err := caps.Resolve(ResolveEnv{
		Home:   "/home/u",
		Getenv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		Conns: []map[string]any{
			{"url": "https://one.example.net", "relay": map[string]any{"addr": "relay.example.org"}, "hook": map[string]any{"listen": ":8080"}, "key_path": "~/keys/app.pem"},
			{"url": "http://two.example.net:8000/x", "state_dir": "/srv/state"},
			{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Grant{
		Egress:   []string{"*.cdn.example.com:443", "api.example.com:443", "bare.example.com:443", "one.example.net:443", "relay.example.org:8443", "two.example.net:8000"},
		Listen:   []string{"127.0.0.1:9000", ":8080"},
		Read:     []string{"/etc/tool.conf", "/home/u/.config/tool", "/home/u/keys/app.pem", "/opt/tool", "/opt/tool/sub"},
		Write:    []string{"/srv/state", "/var/lib/tool"},
		Commands: []string{"tool"},
		Env:      []string{"TOOL_DIR", "TOOL_TOKEN"},
	}
	if !g.FromConfig("read", "/home/u/keys/app.pem") || g.FromConfig("read", "/etc/tool.conf") {
		t.Fatalf("origins: %v", g.From)
	}
	g.From = nil
	if !reflect.DeepEqual(g, want) {
		t.Fatalf("grant:\n got %+v\nwant %+v", g, want)
	}

	// No configured instance (a type-level describe): config: entries are nothing.
	g, err = caps.Resolve(ResolveEnv{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(g.Egress, ","), "example.net") || len(g.Write) != 1 || len(g.Read) != 1 {
		t.Fatalf("no instances should resolve only static entries: %+v", g)
	}
}

func TestCapabilitiesResolveCommandTree(t *testing.T) {
	caps := Capabilities{Commands: []string{"tool", "absent"}, Read: []string{"command:tool/../..", "command:absent/.."}}
	if err := caps.Validate(nil); err != nil {
		t.Fatal(err)
	}
	g, err := caps.Resolve(ResolveEnv{Command: func(n string) (string, bool) {
		if n == "tool" {
			return "/opt/pkgs/tool/bin/tool.js", true
		}
		return "", false
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g.Read, []string{"/opt/pkgs/tool"}) {
		t.Fatalf("read = %v, want the tool's install tree only", g.Read)
	}
}

func TestCapabilitiesResolveRefusesWhatItCannotRead(t *testing.T) {
	caps := Capabilities{Egress: []string{"config:url"}, Read: []string{"config:key"}}
	for name, conn := range map[string]map[string]any{
		"glob host":     {"url": "https://*.example.com"},
		"no port":       {"url": "example.com"},
		"relative path": {"key": "keys/app.pem"},
		"no host":       {"url": "https://"},
	} {
		if _, err := caps.Resolve(ResolveEnv{Conns: []map[string]any{conn}}); err == nil {
			t.Errorf("%s: resolved, want an error", name)
		}
	}
}

func TestCapabilitiesValidate(t *testing.T) {
	conn := Schema{"url": {}}
	for name, caps := range map[string]Capabilities{
		"undeclared field":   {Egress: []string{"config:nope"}},
		"undeclared env ref": {Read: []string{"env:HOME"}},
		"relative path":      {Write: []string{"data"}},
		"command path":       {Commands: []string{"/usr/bin/git"}},
		"undeclared command": {Read: []string{"command:tool/.."}},
		"bad port":           {Egress: []string{"api.example.com:http"}},
		"bad listen":         {Listen: []string{"localhost"}},
	} {
		if err := caps.Validate(conn); err == nil {
			t.Errorf("%s: validated, want an error", name)
		}
	}
}

// A config: reference may not name a secret field: a secret's value is never
// a destination or a path.
func TestCapabilitiesValidateRefusesSecretField(t *testing.T) {
	conn := Schema{"token": {Type: "string", Secret: true}, "url": {Type: "string"}}
	for _, caps := range []Capabilities{
		{Egress: []string{"config:token"}},
		{Read: []string{"config:token"}},
		{Listen: []string{"config:token"}},
	} {
		if err := caps.Validate(conn); err == nil || !strings.Contains(err.Error(), "secret") {
			t.Errorf("%+v: %v", caps, err)
		}
	}
	if err := (Capabilities{Egress: []string{"config:url"}}).Validate(conn); err != nil {
		t.Fatal(err)
	}
}

// A configured destination may not carry anything the egress proxy's
// matcher reads as a pattern.
func TestConfiguredHostMayNotGlob(t *testing.T) {
	caps := Capabilities{Egress: []string{"config:url"}}
	for _, v := range []string{"https://*.example.com", "https://[a-z].example.com", "api.ex[am]ple.com:443", "a?.example.com:443", `api\.example.com`} {
		if _, err := caps.Resolve(ResolveEnv{Conns: []map[string]any{{"url": v}}}); err == nil {
			t.Errorf("%q was granted", v)
		}
	}
	if g, err := caps.Resolve(ResolveEnv{Conns: []map[string]any{{"url": "https://[::1]:8443/x"}}}); err != nil || len(g.Egress) != 1 {
		t.Fatalf("an IPv6 literal URL: %+v %v", g, err)
	}
}

// exec: entries resolve like read entries, command: relative to the tool.
func TestCapabilitiesResolveExec(t *testing.T) {
	caps := Capabilities{Commands: []string{"git"}, Exec: []string{"command:git/../../lib/git-core", "command:git/../../libexec/git-core", "/opt/tool/bin"}}
	if err := caps.Validate(nil); err != nil {
		t.Fatal(err)
	}
	g, err := caps.Resolve(ResolveEnv{Command: func(n string) (string, bool) { return "/usr/bin/git", n == "git" }})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/tool/bin", "/usr/lib/git-core", "/usr/libexec/git-core"}
	if !reflect.DeepEqual(g.Exec, want) {
		t.Fatalf("exec = %v, want %v", g.Exec, want)
	}
	if err := (Capabilities{Exec: []string{"command:git/../lib"}}).Validate(nil); err == nil {
		t.Fatal("exec relative to an undeclared command was accepted")
	}
}

// A "?" entry is optional; a command: path may not leave the command's
// install tree (its directory, or the prefix above bin/).
func TestCapabilitiesOptionalAndInstallTree(t *testing.T) {
	caps := Capabilities{Commands: []string{"tool"}, Exec: []string{"?command:tool/..", "command:tool/../../lib/tool"}}
	if err := caps.Validate(nil); err != nil {
		t.Fatal(err)
	}
	cmd := func(string) (string, bool) { return "/usr/bin/tool", true }
	g, err := caps.Resolve(ResolveEnv{Command: cmd})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g.Exec, []string{"/usr/bin", "/usr/lib/tool"}) || !g.Optional("exec", "/usr/bin") || g.Optional("exec", "/usr/lib/tool") {
		t.Fatalf("exec %v from %v", g.Exec, g.From)
	}
	for _, e := range []string{"command:tool/../../..", "command:tool/../../../etc"} {
		if _, err := (Capabilities{Commands: []string{"tool"}, Read: []string{e}}).Resolve(ResolveEnv{Command: cmd}); err == nil || !strings.Contains(err.Error(), "install tree") {
			t.Errorf("%s: %v, want outside the install tree", e, err)
		}
	}
	if err := (Capabilities{Egress: []string{"?api.example.com"}}).Validate(nil); err == nil {
		t.Error("an optional egress entry validated")
	}
}
