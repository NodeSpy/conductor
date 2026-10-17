package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// An agent container is the agent sandbox's container backend: the agent
// runs in a container of its own instead of the namespace/Seatbelt jail —
// with a persistent one per identity, its own computer. The container has no
// network (`--network none`) and no capabilities, runs as a non-root user
// with no-new-privileges and the engine's default seccomp profile, and shows
// exactly the mounts conductor lists here: the workspace, the identity's
// HOME, and conductor's own (static) binary, read-only. Every connection in
// or out crosses the stdio mux (mux.go). Nothing else of the host is in it —
// no engine socket, ever.

const (
	// ContainerHelperDir is where conductor's binary appears in an agent
	// container (bound read-only from the host).
	ContainerHelperDir = "/run/conductor/bin"
	// ContainerSockDir is the container's socket dir (a sticky tmpfs —
	// the agent's is the container's only user): the conductor socket's
	// in-container end, and the launches' pid files.
	ContainerSockDir = "/run/conductor/s"
	// ContainerHome is the agent's HOME inside its container.
	ContainerHome = "/home/agent"
)

// ContainerMount is one mount of an agent container. Bind mounts appear at
// the same path as on the host (Dst "" = Src).
type ContainerMount struct {
	Src    string // host path (bind), volume name (Volume), "" (Tmpfs)
	Dst    string
	RO     bool
	Volume bool
	Tmpfs  bool
}

// AgentContainer is how conductor creates an agent's container.
type AgentContainer struct {
	Engine string // docker | podman (binary name or path)
	Name   string
	Image  string
	Labels map[string]string
	// User is "uid:gid" — never root.
	User string
	// KeepID maps the host user to the same uid inside (rootless podman).
	KeepID bool
	// HomeVolume (a named volume — persistent) or HomeBind (a host dir) is
	// the agent's HOME, at ContainerHome.
	HomeVolume string
	HomeBind   string
	// Mounts are the workspace and its git paths, the login files, hidden
	// (tmpfs) paths.
	Mounts []ContainerMount
	// Helper is the host directory holding conductor's (Linux) binary,
	// bound read-only at ContainerHelperDir; "" = HelperInImage, the
	// image's own copy (macOS, where the host binary is not a Linux one).
	Helper        string
	HelperInImage string
	ReadOnlyRoot  bool
	Memory, CPU   string
	Pids          int
	// Env are create-time variables — never a secret (they show in the
	// engine's inspect): HOME, PATH, the package-manager prefixes.
	Env []string
	// PullNever refuses to fetch the image (conductor's own local image).
	PullNever bool
}

// HelperPath is conductor's binary as the container sees it.
func (c AgentContainer) HelperPath() string {
	if c.Helper != "" {
		return ContainerHelperDir + "/conductor"
	}
	return c.HelperInImage
}

// uidGid parses c.User.
func (c AgentContainer) uidGid() (int, int, error) {
	u, g, ok := strings.Cut(c.User, ":")
	uid, err1 := strconv.Atoi(u)
	gid, err2 := strconv.Atoi(g)
	if !ok || err1 != nil || err2 != nil || uid < 0 || gid < 0 {
		return 0, 0, fmt.Errorf("sandbox: container user %q is not uid:gid", c.User)
	}
	if uid == 0 {
		return 0, 0, fmt.Errorf("sandbox: an agent container never runs as root (user %q)", c.User)
	}
	return uid, gid, nil
}

// CreateArgs is the engine argv that creates the container (stopped).
func (c AgentContainer) CreateArgs() ([]string, error) {
	if c.Engine == "" || c.Name == "" || c.Image == "" {
		return nil, fmt.Errorf("sandbox: an agent container needs an engine, a name and an image")
	}
	if _, _, err := c.uidGid(); err != nil {
		return nil, err
	}
	if c.HelperPath() == "" {
		return nil, fmt.Errorf("sandbox: an agent container needs conductor's binary inside it")
	}
	if (c.HomeVolume == "") == (c.HomeBind == "") {
		return nil, fmt.Errorf("sandbox: an agent container needs exactly one HOME (a volume or a host dir)")
	}
	args := []string{c.Engine, "create", "--name", c.Name, "--hostname", "conductor-agent"}
	keys := make([]string, 0, len(c.Labels))
	for k := range c.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--label", k+"="+c.Labels[k])
	}
	args = append(args,
		"--network", "none",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", c.User)
	if c.KeepID {
		args = append(args, "--userns", "keep-id")
	}
	if c.ReadOnlyRoot {
		args = append(args, "--read-only")
	}
	args = append(args,
		"--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777",
		"--tmpfs", ContainerSockDir+":rw,nosuid,nodev,noexec,mode=1777")
	if c.Pids > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(c.Pids))
	}
	if c.Memory != "" {
		args = append(args, "--memory", c.Memory)
	}
	if c.CPU != "" {
		args = append(args, "--cpus", c.CPU)
	}
	home := ContainerMount{Src: c.HomeBind, Dst: ContainerHome}
	if c.HomeVolume != "" {
		home = ContainerMount{Src: c.HomeVolume, Dst: ContainerHome, Volume: true}
	}
	mounts := append([]ContainerMount{home}, c.Mounts...)
	if c.Helper != "" {
		mounts = append(mounts, ContainerMount{Src: c.Helper, Dst: ContainerHelperDir, RO: true})
	}
	for _, m := range mounts {
		spec, err := m.spec()
		if err != nil {
			return nil, err
		}
		args = append(args, "--mount", spec)
	}
	if c.PullNever {
		args = append(args, "--pull", "never")
	}
	for _, kv := range c.Env {
		args = append(args, "--env", kv)
	}
	args = append(args, "--entrypoint", c.HelperPath(), c.Image, "sandbox-net", "--init")
	if err := CheckContainerArgs(args); err != nil {
		return nil, err
	}
	return args, nil
}

// SeedArgs is the one-shot run that fills a fresh HOME volume from seed (a
// host dir: the identity home conductor prepared) and gives it to the
// agent's user — the only container conductor runs as root, with nothing
// but the volume, the seed (read-only) and the helper, and only the
// capabilities a copy-and-chown needs.
func (c AgentContainer) SeedArgs(seed string) ([]string, error) {
	uid, gid, err := c.uidGid()
	if err != nil {
		return nil, err
	}
	if c.HomeVolume == "" {
		return nil, fmt.Errorf("sandbox: only a HOME volume is seeded")
	}
	args := []string{c.Engine, "run", "--rm", "--name", c.Name + "-seed"}
	keys := make([]string, 0, len(c.Labels))
	for k := range c.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--label", k+"="+c.Labels[k])
	}
	args = append(args, "--network", "none", "--cap-drop", "ALL")
	for _, cp := range seedCaps {
		args = append(args, "--cap-add", cp)
	}
	args = append(args, "--security-opt", "no-new-privileges", "--user", "0:0")
	if c.KeepID {
		args = append(args, "--userns", "keep-id")
	}
	mounts := []ContainerMount{{Src: c.HomeVolume, Dst: "/dst", Volume: true}, {Src: seed, Dst: "/seed", RO: true}}
	if c.Helper != "" {
		mounts = append(mounts, ContainerMount{Src: c.Helper, Dst: ContainerHelperDir, RO: true})
	}
	for _, m := range mounts {
		spec, err := m.spec()
		if err != nil {
			return nil, err
		}
		args = append(args, "--mount", spec)
	}
	if c.PullNever {
		args = append(args, "--pull", "never")
	}
	args = append(args, "--entrypoint", c.HelperPath(), c.Image,
		"sandbox-net", "--seed", "/seed", "/dst", strconv.Itoa(uid), strconv.Itoa(gid))
	if err := checkArgs(args, true); err != nil {
		return nil, err
	}
	return args, nil
}

// seedCaps are the seed run's capabilities: copy files it may not own and
// hand them to the agent's user.
var seedCaps = []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"}

// ContainerExecArgs is the engine argv that runs argv in a running agent
// container as user, in workdir: explicit are KEY=VALUE pairs given on the
// command line (never a secret — HOME, TMPDIR), pass are keys the engine
// client passes through from its own environment (the token, the model
// credential, the proxy URL with its credential: never on a command line).
func ContainerExecArgs(engine, name, user, workdir string, explicit, pass, argv []string) ([]string, error) {
	if engine == "" || name == "" || len(argv) == 0 {
		return nil, fmt.Errorf("sandbox: container exec needs an engine, a container and a command")
	}
	if u, _, _ := strings.Cut(user, ":"); u == "" || u == "0" || u == "root" {
		return nil, fmt.Errorf("sandbox: an agent never runs as root in its container (user %q)", user)
	}
	args := []string{engine, "exec", "-i", "--user", user}
	if workdir != "" {
		args = append(args, "--workdir", workdir)
	}
	for _, kv := range explicit {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("sandbox: container env %q is not KEY=VALUE", kv)
		}
		args = append(args, "--env", kv)
	}
	for _, k := range pass {
		if k == "" || strings.Contains(k, "=") {
			return nil, fmt.Errorf("sandbox: container env key %q", k)
		}
		args = append(args, "--env", k)
	}
	args = append(args, name)
	return append(args, argv...), nil
}

// CheckContainerArgs is the agent container's invariant, checked on the
// argv conductor generated before it ever reaches the engine: no
// --privileged, no capability added back, no host namespace, no device, no
// engine socket or socket of any kind, no `-v` (every mount is a --mount
// conductor wrote), the network none, every capability dropped,
// no-new-privileges on, never root.
func CheckContainerArgs(args []string) error { return checkArgs(args, false) }

func checkArgs(args []string, seed bool) error {
	var network, capDrop, nnp, user bool
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		flag, inline, hasInline := strings.Cut(a, "=")
		val := inline
		if !hasInline && strings.HasPrefix(a, "--") {
			val = next()
		}
		switch flag {
		case "--privileged":
			return fmt.Errorf("sandbox: an agent container is never --privileged")
		case "-v", "--volume", "--volumes-from", "--device", "--device-cgroup-rule", "--group-add", "--cgroupns", "--uts", "--ipc", "--pid", "--add-host", "--gpus":
			return fmt.Errorf("sandbox: an agent container takes no %s", flag)
		case "--cap-add":
			if !seed || !contains(seedCaps, val) {
				return fmt.Errorf("sandbox: an agent container adds no capability (%s)", val)
			}
		case "--cap-drop":
			capDrop = capDrop || val == "ALL"
		case "--network", "--net":
			if val != "none" {
				return fmt.Errorf("sandbox: an agent container's network is none, not %q", val)
			}
			network = true
		case "--security-opt":
			v := strings.ToLower(val)
			if strings.Contains(v, "unconfined") || strings.Contains(v, "=false") || strings.Contains(v, ":false") || strings.HasPrefix(v, "label=disable") {
				return fmt.Errorf("sandbox: an agent container keeps its security options (%s)", val)
			}
			nnp = nnp || v == "no-new-privileges" || v == "no-new-privileges:true" || v == "no-new-privileges=true"
		case "--user", "-u":
			u, _, _ := strings.Cut(val, ":")
			if !seed && (u == "0" || u == "root" || u == "") {
				return fmt.Errorf("sandbox: an agent container never runs as root")
			}
			user = true
		case "--userns":
			if val != "keep-id" {
				return fmt.Errorf("sandbox: an agent container's user namespace is its own (not %q)", val)
			}
		case "--mount":
			if err := checkMountSpec(val); err != nil {
				return err
			}
		}
	}
	switch {
	case !network:
		return fmt.Errorf("sandbox: an agent container needs --network none")
	case !capDrop:
		return fmt.Errorf("sandbox: an agent container drops every capability")
	case !nnp:
		return fmt.Errorf("sandbox: an agent container runs with no-new-privileges")
	case !user:
		return fmt.Errorf("sandbox: an agent container names its user")
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// spec renders a --mount value, refusing a source that must never be in
// an agent container.
func (m ContainerMount) spec() (string, error) {
	dst := m.Dst
	if dst == "" {
		dst = m.Src
	}
	for _, p := range []string{m.Src, dst} {
		if strings.ContainsAny(p, ",\"\n\r") {
			return "", fmt.Errorf("sandbox: container mount path %q has a character a --mount cannot carry", p)
		}
	}
	if !filepath.IsAbs(dst) {
		return "", fmt.Errorf("sandbox: container mount target %q is not absolute", dst)
	}
	var s string
	switch {
	case m.Tmpfs:
		s = "type=tmpfs,dst=" + dst
	case m.Volume:
		if m.Src == "" || strings.ContainsAny(m.Src, "/:") {
			return "", fmt.Errorf("sandbox: container volume %q is not a volume name", m.Src)
		}
		s = "type=volume,src=" + m.Src + ",dst=" + dst
	default:
		if err := allowedBindSource(m.Src); err != nil {
			return "", err
		}
		s = "type=bind,src=" + m.Src + ",dst=" + dst
	}
	if m.RO {
		s += ",readonly"
	}
	return s, checkMountSpec(s)
}

// checkMountSpec re-checks a rendered --mount value (the invariant scan).
func checkMountSpec(spec string) error {
	fields := map[string]string{}
	for _, f := range strings.Split(spec, ",") {
		k, v, _ := strings.Cut(f, "=")
		fields[k] = v
	}
	if _, ok := fields["bind-propagation"]; ok {
		return fmt.Errorf("sandbox: container mount %q sets a propagation", spec)
	}
	switch fields["type"] {
	case "bind":
		src := fields["src"]
		if src == "" {
			src = fields["source"]
		}
		return allowedBindSource(src)
	case "volume", "tmpfs":
		return nil
	default:
		return fmt.Errorf("sandbox: container mount type %q", fields["type"])
	}
}

// EngineSocketPaths are where container engines' control sockets live.
// None is ever mounted into an agent container, nor a directory holding
// one: with it the agent could start a privileged container of its own.
func EngineSocketPaths() []string {
	out := []string{
		"/var/run/docker.sock", "/run/docker.sock",
		"/run/podman/podman.sock", "/var/run/podman/podman.sock",
		"/run/containerd/containerd.sock", "/var/run/containerd/containerd.sock",
		"/run/crio/crio.sock", "/var/run/crio/crio.sock",
		"/run/buildkit/buildkitd.sock",
	}
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		out = append(out, filepath.Join(x, "docker.sock"), filepath.Join(x, "podman", "podman.sock"))
	}
	out = append(out, fmt.Sprintf("/run/user/%d/docker.sock", os.Getuid()), fmt.Sprintf("/run/user/%d/podman/podman.sock", os.Getuid()))
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		out = append(out,
			filepath.Join(h, ".docker", "run", "docker.sock"),
			filepath.Join(h, ".docker", "desktop", "docker.sock"),
			filepath.Join(h, ".colima", "default", "docker.sock"),
			filepath.Join(h, ".lima", "default", "sock", "docker.sock"),
			filepath.Join(h, ".orbstack", "run", "docker.sock"),
			filepath.Join(h, ".local", "share", "containers", "podman", "machine", "podman.sock"))
	}
	if dh := os.Getenv("DOCKER_HOST"); strings.HasPrefix(dh, "unix://") {
		out = append(out, strings.TrimPrefix(dh, "unix://"))
	}
	return out
}

// hostSystemDirs are host directories never bound into an agent container
// whole: each is (or holds) the host's own system, devices, or runtime
// sockets.
var hostSystemDirs = []string{"/", "/run", "/var/run", "/var", "/proc", "/sys", "/dev", "/etc", "/boot", "/root", "/usr", "/var/lib", "/private", "/private/var", "/private/var/run", "/Users", "/home", "/tmp", "/private/tmp"}

// allowedBindSource refuses a bind source that is a socket, an engine's
// socket or a directory containing one, or a host system directory.
func allowedBindSource(src string) error {
	if src == "" || !filepath.IsAbs(src) {
		return fmt.Errorf("sandbox: container bind source %q is not an absolute path", src)
	}
	clean := filepath.Clean(src)
	cands := []string{clean}
	if r, err := filepath.EvalSymlinks(clean); err == nil && r != clean {
		cands = append(cands, r)
	}
	for _, p := range cands {
		if fi, err := os.Stat(p); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return fmt.Errorf("sandbox: %s is a socket — no socket is ever mounted into an agent container", src)
		}
		if isHostSystemDir(p) {
			return fmt.Errorf("sandbox: %s is a host system directory — never mounted into an agent container", src)
		}
		for _, s := range EngineSocketPaths() {
			if within(s, p) {
				return fmt.Errorf("sandbox: %s is or holds the container engine's socket (%s) — never mounted into an agent container", src, s)
			}
		}
	}
	return nil
}

// isHostSystemDir reports p is one of hostSystemDirs as it resolves on this
// host (macOS's /etc, /tmp and /var are symlinks into /private). The caller
// passes a bind source's resolved path among its candidates.
func isHostSystemDir(p string) bool {
	for _, d := range hostSystemDirs {
		if r, err := filepath.EvalSymlinks(d); err == nil {
			d = r
		}
		if p == d {
			return true
		}
	}
	return false
}

// within reports p == dir or p inside dir.
func within(p, dir string) bool {
	if p == dir {
		return true
	}
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}
