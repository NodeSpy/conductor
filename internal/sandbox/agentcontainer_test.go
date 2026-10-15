package sandbox

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testContainer(t *testing.T) AgentContainer {
	t.Helper()
	ws := t.TempDir()
	helper := t.TempDir()
	return AgentContainer{
		Engine: "docker", Name: "conductor-x-a-1", Image: "img:1",
		Labels: map[string]string{"conductor.sandbox": "agent", "conductor.daemon": "d"},
		User:   "1000:1000", HomeVolume: "conductor-x-home-a",
		Mounts: []ContainerMount{{Src: ws}},
		Helper: helper,
		Env:    []string{"HOME=" + ContainerHome},
	}
}

func argValue(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func hasArg(args []string, a string) bool {
	for _, x := range args {
		if x == a {
			return true
		}
	}
	return false
}

// The create argv carries every hardening flag, exactly the listed mounts,
// and nothing that loosens the container.
func TestAgentContainerCreateArgs(t *testing.T) {
	c := testContainer(t)
	c.ReadOnlyRoot, c.Pids, c.Memory, c.CPU = true, 256, "2g", "1.5"
	args, err := c.CreateArgs()
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != "docker" || args[1] != "create" {
		t.Fatalf("argv %v", args[:2])
	}
	for flag, want := range map[string]string{
		"--network": "none", "--cap-drop": "ALL", "--security-opt": "no-new-privileges", "--user": "1000:1000",
		"--pids-limit": "256", "--memory": "2g", "--cpus": "1.5", "--entrypoint": ContainerHelperDir + "/conductor", "--name": "conductor-x-a-1",
	} {
		if got := argValue(args, flag); len(got) != 1 || got[0] != want {
			t.Errorf("%s = %v, want [%s]", flag, got, want)
		}
	}
	if !hasArg(args, "--read-only") {
		t.Error("read-only root missing")
	}
	for _, bad := range []string{"--privileged", "--cap-add", "-v", "--volume", "--device", "--pid", "--ipc", "--userns"} {
		if hasArg(args, bad) {
			t.Errorf("%s in the create argv", bad)
		}
	}
	mounts := argValue(args, "--mount")
	if len(mounts) != 3 {
		t.Fatalf("mounts %v, want HOME, workspace, helper", mounts)
	}
	if mounts[0] != "type=volume,src=conductor-x-home-a,dst="+ContainerHome {
		t.Errorf("HOME mount %q", mounts[0])
	}
	if mounts[1] != "type=bind,src="+c.Mounts[0].Src+",dst="+c.Mounts[0].Src {
		t.Errorf("workspace mount %q", mounts[1])
	}
	if mounts[2] != "type=bind,src="+c.Helper+",dst="+ContainerHelperDir+",readonly" {
		t.Errorf("helper mount %q", mounts[2])
	}
	tmpfs := argValue(args, "--tmpfs")
	if len(tmpfs) != 2 || tmpfs[0] != "/tmp:rw,nosuid,nodev,mode=1777" || tmpfs[1] != ContainerSockDir+":rw,nosuid,nodev,noexec,mode=1777" {
		t.Errorf("tmpfs %v", tmpfs)
	}
	if got := argValue(args, "--label"); len(got) != 2 || got[0] != "conductor.daemon=d" || got[1] != "conductor.sandbox=agent" {
		t.Errorf("labels %v (sorted, all of them)", got)
	}
	n := len(args)
	if args[n-3] != "img:1" || args[n-2] != "sandbox-net" || args[n-1] != "--init" {
		t.Errorf("tail %v", args[n-3:])
	}
	if hasArg(args, "--pull") {
		t.Error("an operator image may be pulled")
	}
	c.PullNever = true
	if args, _ := c.CreateArgs(); len(argValue(args, "--pull")) != 1 || argValue(args, "--pull")[0] != "never" {
		t.Error("conductor's own image is never pulled")
	}
	c.KeepID = true
	if args, _ := c.CreateArgs(); len(argValue(args, "--userns")) != 1 || argValue(args, "--userns")[0] != "keep-id" {
		t.Error("rootless podman keeps the user's id")
	}
	c.ReadOnlyRoot = false
	if args, _ := c.CreateArgs(); hasArg(args, "--read-only") {
		t.Error("read-only root without asking")
	}
}

// A HOME bind (a per-launch container) and the image's own helper.
func TestAgentContainerHomeBindAndImageHelper(t *testing.T) {
	c := testContainer(t)
	c.HomeVolume, c.HomeBind = "", t.TempDir()
	c.Helper, c.HelperInImage = "", "/usr/local/bin/conductor"
	args, err := c.CreateArgs()
	if err != nil {
		t.Fatal(err)
	}
	mounts := argValue(args, "--mount")
	if len(mounts) != 2 || mounts[0] != "type=bind,src="+c.HomeBind+",dst="+ContainerHome {
		t.Errorf("mounts %v", mounts)
	}
	if got := argValue(args, "--entrypoint"); got[0] != "/usr/local/bin/conductor" {
		t.Errorf("entrypoint %v", got)
	}
	c.HomeVolume = "v"
	if _, err := c.CreateArgs(); err == nil {
		t.Error("two HOMEs accepted")
	}
	c.HomeVolume, c.HomeBind = "", ""
	if _, err := c.CreateArgs(); err == nil {
		t.Error("no HOME accepted")
	}
	c = testContainer(t)
	c.Helper, c.HelperInImage = "", ""
	if _, err := c.CreateArgs(); err == nil {
		t.Error("no helper accepted")
	}
}

// Never root, never an incomplete container.
func TestAgentContainerRefusals(t *testing.T) {
	for name, mut := range map[string]func(*AgentContainer){
		"root":           func(c *AgentContainer) { c.User = "0:0" },
		"uid only":       func(c *AgentContainer) { c.User = "1000" },
		"named user":     func(c *AgentContainer) { c.User = "root:root" },
		"negative":       func(c *AgentContainer) { c.User = "-1:5" },
		"no engine":      func(c *AgentContainer) { c.Engine = "" },
		"no name":        func(c *AgentContainer) { c.Name = "" },
		"no image":       func(c *AgentContainer) { c.Image = "" },
		"comma in path":  func(c *AgentContainer) { c.Mounts = append(c.Mounts, ContainerMount{Src: "/srv/a,b"}) },
		"relative mount": func(c *AgentContainer) { c.Mounts = append(c.Mounts, ContainerMount{Src: "rel"}) },
		"volume path":    func(c *AgentContainer) { c.HomeVolume = "/var/lib/x" },
		"root fs":        func(c *AgentContainer) { c.Mounts = append(c.Mounts, ContainerMount{Src: "/"}) },
		"etc":            func(c *AgentContainer) { c.Mounts = append(c.Mounts, ContainerMount{Src: "/etc"}) },
		"var run":        func(c *AgentContainer) { c.Mounts = append(c.Mounts, ContainerMount{Src: "/var/run"}) },
		"run":            func(c *AgentContainer) { c.Mounts = append(c.Mounts, ContainerMount{Src: "/run"}) },
		"docker sock":    func(c *AgentContainer) { c.Mounts = append(c.Mounts, ContainerMount{Src: "/var/run/docker.sock"}) },
		"podman dir":     func(c *AgentContainer) { c.Mounts = append(c.Mounts, ContainerMount{Src: "/run/podman"}) },
		"home dir":       func(c *AgentContainer) { c.HomeVolume, c.HomeBind = "", "/home" },
		"helper is /run": func(c *AgentContainer) { c.Helper = "/run" },
	} {
		c := testContainer(t)
		mut(&c)
		if _, err := c.CreateArgs(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A socket — any socket, an engine's above all — is never bound in, nor a
// directory holding the engine's.
func TestAgentContainerNoSocketMount(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "x.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c := testContainer(t)
	c.Mounts = append(c.Mounts, ContainerMount{Src: sock, Dst: "/x.sock"})
	if _, err := c.CreateArgs(); err == nil || !strings.Contains(err.Error(), "socket") {
		t.Errorf("a socket bound in: %v", err)
	}
	// A symlink to a socket is the socket.
	link := filepath.Join(dir, "link")
	_ = os.Symlink(sock, link)
	c = testContainer(t)
	c.Mounts = append(c.Mounts, ContainerMount{Src: link})
	if _, err := c.CreateArgs(); err == nil {
		t.Error("a symlink to a socket bound in")
	}
	// DOCKER_HOST's socket, and the directory holding it.
	engineDir := t.TempDir()
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(engineDir, "engine.sock"))
	c = testContainer(t)
	c.Mounts = append(c.Mounts, ContainerMount{Src: engineDir})
	if _, err := c.CreateArgs(); err == nil || !strings.Contains(err.Error(), "engine") {
		t.Errorf("the engine's socket dir bound in: %v", err)
	}
	// A rootless engine's runtime dir.
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	c = testContainer(t)
	c.Mounts = append(c.Mounts, ContainerMount{Src: xdg})
	if _, err := c.CreateArgs(); err == nil {
		t.Error("XDG_RUNTIME_DIR (a rootless engine's socket dir) bound in")
	}
}

// The invariant scan on its own: whatever produced the argv, these never
// reach the engine.
func TestCheckContainerArgs(t *testing.T) {
	good, err := testContainer(t).CreateArgs()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckContainerArgs(good); err != nil {
		t.Fatalf("good argv refused: %v", err)
	}
	if good[len(good)-5] != "--entrypoint" {
		t.Fatalf("argv tail changed: %v", good[len(good)-5:])
	}
	// Insert before "--entrypoint PATH IMAGE sandbox-net --init".
	with := func(extra ...string) []string {
		n := len(good)
		out := append([]string(nil), good[:n-5]...)
		out = append(out, extra...)
		return append(out, good[n-5:]...)
	}
	okDir := t.TempDir()
	without := func(flag string) []string {
		var out []string
		for i := 0; i < len(good); i++ {
			if good[i] == flag {
				i++
				continue
			}
			out = append(out, good[i])
		}
		return out
	}
	for name, args := range map[string][]string{
		"privileged":          with("--privileged"),
		"privileged=true":     with("--privileged=true"),
		"cap-add":             with("--cap-add", "SYS_ADMIN"),
		"cap-add=":            with("--cap-add=NET_ADMIN"),
		"-v":                  with("-v", "/var/run/docker.sock:/var/run/docker.sock"),
		"--volume":            with("--volume", "/x:/x"),
		"--volume=":           with("--volume=/x:/x"),
		"volumes-from":        with("--volumes-from", "other"),
		"device":              with("--device", "/dev/kvm"),
		"pid host":            with("--pid", "host"),
		"ipc host":            with("--ipc", "host"),
		"uts host":            with("--uts", "host"),
		"network host":        with("--network", "host"),
		"net=host":            with("--net=host"),
		"userns host":         with("--userns", "host"),
		"seccomp unconfined":  with("--security-opt", "seccomp=unconfined"),
		"apparmor unconfined": with("--security-opt", "apparmor=unconfined"),
		"nnp false":           with("--security-opt", "no-new-privileges=false"),
		"label disable":       with("--security-opt", "label=disable"),
		"root user":           with("--user", "0"),
		"group-add":           with("--group-add", "docker"),
		"docker.sock mount":   with("--mount", "type=bind,src=/var/run/docker.sock,dst=/s"),
		"source= spelling":    with("--mount", "type=bind,source=/run,dst=/r"),
		"propagation":         with("--mount", "type=bind,src="+okDir+",dst=/p,bind-propagation=rshared"),
		"npipe":               with("--mount", "type=npipe,src=x,dst=/x"),
		"no network":          without("--network"),
		"no cap-drop":         without("--cap-drop"),
		"no nnp":              without("--security-opt"),
		"no user":             without("--user"),
		"gpus":                with("--gpus", "all"),
		"add-host":            with("--add-host", "h:1.2.3.4"),
		"cgroupns host":       with("--cgroupns", "host"),
	} {
		if err := CheckContainerArgs(args); err == nil {
			t.Errorf("%s: passed the invariant", name)
		}
	}
	// The seed may add exactly its capabilities, run as root — nothing else.
	if err := CheckContainerArgs(with("--mount", "type=bind,src="+okDir+",dst=/p")); err != nil {
		t.Errorf("a plain bind refused: %v", err)
	}
	if err := checkArgs(with("--cap-add", "CHOWN"), true); err != nil {
		t.Errorf("seed's CHOWN refused: %v", err)
	}
	if err := checkArgs(with("--cap-add", "SYS_ADMIN"), true); err == nil {
		t.Error("seed added SYS_ADMIN")
	}
	if err := checkArgs(with("--cap-add", "CHOWN"), false); err == nil {
		t.Error("an agent container added CHOWN")
	}
}

// The seed run: root with only copy-and-chown capabilities, nothing but the
// volume, the seed (read-only) and the helper.
func TestAgentContainerSeedArgs(t *testing.T) {
	c := testContainer(t)
	seed := t.TempDir()
	args, err := c.SeedArgs(seed)
	if err != nil {
		t.Fatal(err)
	}
	if args[1] != "run" || !hasArg(args, "--rm") {
		t.Errorf("not a one-shot run: %v", args[:4])
	}
	if got := argValue(args, "--cap-add"); strings.Join(got, ",") != "CHOWN,DAC_OVERRIDE,FOWNER" {
		t.Errorf("caps %v", got)
	}
	if got := argValue(args, "--network"); len(got) != 1 || got[0] != "none" {
		t.Errorf("network %v", got)
	}
	mounts := argValue(args, "--mount")
	want := []string{"type=volume,src=conductor-x-home-a,dst=/dst", "type=bind,src=" + seed + ",dst=/seed,readonly", "type=bind,src=" + c.Helper + ",dst=" + ContainerHelperDir + ",readonly"}
	if strings.Join(mounts, "|") != strings.Join(want, "|") {
		t.Errorf("mounts %v, want %v", mounts, want)
	}
	n := len(args)
	if strings.Join(args[n-6:], " ") != "sandbox-net --seed /seed /dst 1000 1000" {
		t.Errorf("tail %v", args[n-6:])
	}
	c.HomeVolume, c.HomeBind = "", seed
	if _, err := c.SeedArgs(seed); err == nil {
		t.Error("seeded a bind HOME")
	}
	c = testContainer(t)
	c.User = "0:0"
	if _, err := c.SeedArgs(seed); err == nil {
		t.Error("seeded a HOME for root")
	}
}

// Secrets ride the client's environment by name; the command line carries
// only what is not one.
func TestContainerExecArgs(t *testing.T) {
	args, err := ContainerExecArgs("podman", "c1", "1000:1000", "/ws", []string{"HOME=/home/agent"}, []string{"ANTHROPIC_API_KEY", "CONDUCTOR_SKILL_TOKEN"}, []string{"/run/conductor/bin/conductor", "sandbox-net", "--run", "id", "--", "claude", "-p"})
	if err != nil {
		t.Fatal(err)
	}
	want := "podman exec -i --user 1000:1000 --workdir /ws --env HOME=/home/agent --env ANTHROPIC_API_KEY --env CONDUCTOR_SKILL_TOKEN c1 /run/conductor/bin/conductor sandbox-net --run id -- claude -p"
	if strings.Join(args, " ") != want {
		t.Errorf("argv\n %s\nwant\n %s", strings.Join(args, " "), want)
	}
	for name, try := range map[string]func() error{
		"root": func() error {
			_, err := ContainerExecArgs("docker", "c", "0:0", "", nil, nil, []string{"x"})
			return err
		},
		"root name": func() error {
			_, err := ContainerExecArgs("docker", "c", "root", "", nil, nil, []string{"x"})
			return err
		},
		"no user": func() error { _, err := ContainerExecArgs("docker", "c", "", "", nil, nil, []string{"x"}); return err },
		"value key": func() error {
			_, err := ContainerExecArgs("docker", "c", "1:1", "", nil, []string{"K=V"}, []string{"x"})
			return err
		},
		"empty key": func() error {
			_, err := ContainerExecArgs("docker", "c", "1:1", "", nil, []string{""}, []string{"x"})
			return err
		},
		"bad pair": func() error {
			_, err := ContainerExecArgs("docker", "c", "1:1", "", []string{"NOEQ"}, nil, []string{"x"})
			return err
		},
		"no command": func() error { _, err := ContainerExecArgs("docker", "c", "1:1", "", nil, nil, nil); return err },
		"no name": func() error {
			_, err := ContainerExecArgs("docker", "", "1:1", "", nil, nil, []string{"x"})
			return err
		},
	} {
		if try() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	args, _ = ContainerExecArgs("docker", "c", "1:1", "", nil, nil, []string{"x"})
	if hasArg(args, "--workdir") {
		t.Error("an empty workdir became a flag")
	}
}

func TestWithin(t *testing.T) {
	for _, c := range []struct {
		p, dir string
		want   bool
	}{{"/a/b", "/a", true}, {"/a", "/a", true}, {"/ab", "/a", false}, {"/", "/a", false}, {"/a/../b", "/a", false}} {
		if got := within(filepath.Clean(c.p), c.dir); got != c.want {
			t.Errorf("within(%q, %q) = %v", c.p, c.dir, got)
		}
	}
}
