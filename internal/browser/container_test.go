package browser

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestSeccompProfile: the container's profile is the engine's default
// widened by exactly what Chromium's namespace sandbox needs, nothing else.
func TestSeccompProfile(t *testing.T) {
	type rule struct {
		Names    []string `json:"names"`
		Action   string   `json:"action"`
		ErrnoRet *int     `json:"errnoRet"`
		Args     []any    `json:"args"`
		Includes any      `json:"includes"`
		Excludes any      `json:"excludes"`
	}
	type profile struct {
		DefaultAction string `json:"defaultAction"`
		Syscalls      []rule `json:"syscalls"`
	}
	var def, got profile
	if err := json.Unmarshal(seccompDefault, &def); err != nil {
		t.Fatal(err)
	}
	b, err := SeccompProfile()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.DefaultAction != def.DefaultAction || def.DefaultAction != "SCMP_ACT_ERRNO" {
		t.Fatalf("default action %q (the engine's: %q)", got.DefaultAction, def.DefaultAction)
	}
	widened := []string{"clone", "unshare", "setns", "chroot"}
	key := func(r rule) string {
		x, _ := json.Marshal(struct {
			A, B, C, D any
			E          *int
		}{r.Action, r.Args, r.Includes, r.Excludes, r.ErrnoRet})
		return string(x)
	}
	// Every other syscall keeps exactly the engine's rules.
	rulesFor := func(p profile, name string) []string {
		var out []string
		for _, r := range p.Syscalls {
			if slices.Contains(r.Names, name) {
				out = append(out, key(r))
			}
		}
		slices.Sort(out)
		return out
	}
	names := map[string]bool{}
	for _, p := range []profile{def, got} {
		for _, r := range p.Syscalls {
			for _, n := range r.Names {
				names[n] = true
			}
		}
	}
	for n := range names {
		if slices.Contains(widened, n) || n == "clone3" {
			continue
		}
		if g, d := rulesFor(got, n), rulesFor(def, n); !slices.Equal(g, d) {
			t.Errorf("%s: rules changed: %v (the engine's %v)", n, g, d)
		}
	}
	// The four: allowed outright; clone3 fails with ENOSYS (so libc falls
	// back to clone, whose flags the profile can see).
	for _, n := range widened {
		if r := rulesFor(got, n); len(r) != 1 || r[0] != `{"A":"SCMP_ACT_ALLOW","B":null,"C":null,"D":null,"E":null}` {
			t.Errorf("%s: %v", n, r)
		}
	}
	if r := rulesFor(got, "clone3"); len(r) != 1 || r[0] != `{"A":"SCMP_ACT_ERRNO","B":null,"C":null,"D":null,"E":38}` {
		t.Errorf("clone3: %v", r)
	}
}

// TestContainerArgs: no network, no capability, no privilege gain, a
// read-only root, the seccomp profile, a non-root user, no host mount.
func TestContainerArgs(t *testing.T) {
	a := containerArgs("cb-x", "conductor-browser:abc", "/tmp/seccomp.json", []string{"--headless=new"})
	s := " " + strings.Join(a, " ") + " "
	for _, want := range []string{
		" --network none ", " --cap-drop ALL ", " --security-opt no-new-privileges ",
		" --security-opt seccomp=/tmp/seccomp.json ", " --read-only ", " --user 1000:1000 ",
		" --pids-limit ", " --memory ", " --label conductor.browser=1 ", " --rm ",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
	img := slices.Index(a, "conductor-browser:abc")
	for _, f := range a[:img] {
		for _, bad := range []string{"-v", "--volume", "--mount", "--privileged", "--cap-add", "--net=host", "--network=host", "--pid", "--ipc", "--device", "--userns", "--security-opt=", "unconfined"} {
			if f == bad || strings.HasPrefix(f, bad+"=") || strings.Contains(f, "unconfined") {
				t.Errorf("engine flag %q", f)
			}
		}
	}
	if a[len(a)-1] != "--headless=new" || a[img+1] != "--listen" {
		t.Fatalf("argv: %v", a)
	}
}

// TestUnavailableRefusesEveryLaunch: while the container is being prepared
// (or required and impossible), no browser starts in any weaker mode.
func TestUnavailableRefusesEveryLaunch(t *testing.T) {
	launched := false
	m := &Manager{
		Settings: func() Settings {
			return Settings{Allow: []string{"example.com"}, Chrome: "/nonexistent/chrome", Unavailable: "the browser's container is being prepared",
				Engine: &ContainerEngine{Bin: "/nonexistent/docker", Kind: "docker"}, Image: "conductor-browser:x"}
		},
		StateDir: t.TempDir(),
		Launch:   func(LaunchSpec) (*Process, error) { launched = true; return nil, errors.New("launched") },
	}
	defer m.Close()
	c := Principal{Key: "k", Identity: "agent:test@acme/app", Workspace: t.TempDir(), Requester: RequesterOperator}
	_, err := m.Do(context.Background(), c, "open", map[string]any{"url": "https://example.com/"})
	if err == nil || !strings.Contains(err.Error(), "being prepared") || launched {
		t.Fatalf("open while unavailable: %v (launched %v)", err, launched)
	}
}

func TestSandboxedOK(t *testing.T) {
	for status, want := range map[string]bool{
		"Namespace Sandbox Yes\nSeccomp-BPF sandbox Yes\nYou are adequately sandboxed.": true,
		"Seccomp-BPF sandbox No\nYou are not adequately sandboxed.":                     false,
		"": false, "adequately sandboxed": false,
	} {
		if SandboxedOK(status) != want {
			t.Errorf("%q: %v", status, !want)
		}
	}
}

// TestLaunchSeatbeltWindow: only the operator's headed window gets the
// window server's services.
func TestLaunchSeatbeltWindow(t *testing.T) {
	if p := seatbeltProfile(LaunchSpec{}); !p.Browser || p.Window {
		t.Fatalf("an agent's browser: %+v", p)
	}
	if p := seatbeltProfile(LaunchSpec{Headed: true}); !p.Browser || !p.Window {
		t.Fatalf("the operator's window: %+v", p)
	}
}
