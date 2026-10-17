package agentsandbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// The container backend (isolation `mode: container`): instead of the
// namespace/Seatbelt jail, the agent runs in a container of its own — with
// `persistent: true` one long-lived container per identity, its computer,
// whose HOME is a named volume that keeps what it installs; otherwise one
// per launch, removed when the launch ends, with the identity's tool-state
// directory as HOME (as the jail has it). The guarantees are the jail's:
// the workspace read-write, no secret mounted, the conductor socket
// reachable, no network but conductor's allowlist proxy and the model
// relays, a clean environment and a fresh token per launch, and no launch
// at all when the container cannot be had. Conductor owns the lifecycle:
// create on first use, reuse, stop when idle, remove on archive.

// Container is a policy's container backend.
type Container struct {
	// Engine is docker | podman ("" = rootless podman on Linux, Docker
	// Desktop on macOS, whichever is installed).
	Engine string `json:"engine,omitempty"`
	// Image is the agent's image ("" = conductor's own agent image).
	Image string `json:"image,omitempty"`
	// Persistent keeps one container and HOME volume per identity across
	// launches (stopped when idle, removed on archive).
	Persistent bool `json:"persistent,omitempty"`
	// ReadOnlyRoot mounts the image read-only (HOME, /tmp and the
	// workspace stay writable).
	ReadOnlyRoot bool   `json:"read_only,omitempty"`
	Memory       string `json:"memory,omitempty"`
	CPU          string `json:"cpu,omitempty"`
	Pids         int    `json:"pids,omitempty"`
	// IdleStop stops a persistent container this long after its last
	// launch ends (Go duration; "" = the backend's default).
	IdleStop string `json:"idle_stop,omitempty"`
	// Attach runs the launch in another identity's persistent container (a
	// bot's), which must already exist and show the launch's workspace.
	Attach string `json:"attach,omitempty"`
	// Session is the prepared launch (the daemon fills it in): where and
	// as whom to exec, and the per-launch env.
	Session *ContainerSession `json:"session,omitempty"`
}

// ContainerSession is one launch in a running container.
type ContainerSession struct {
	Engine   string `json:"engine"`
	Name     string `json:"name"`
	User     string `json:"user"`
	LaunchID string `json:"launch_id"`
	// Helper is conductor's binary as the container sees it.
	Helper string `json:"helper"`
	// Env is the launch's own: the proxy URL with its credential, the
	// conductor endpoint (in the container) and the token.
	Env []string `json:"env,omitempty"`
	// Exposed maps each Policy.Expose address to the host unix socket that
	// reaches it.
	Exposed map[string]string `json:"exposed,omitempty"`
}

func (c *Container) clone() *Container {
	if c == nil {
		return nil
	}
	cp := *c
	cp.Session = nil
	return &cp
}

// DefaultAgentImage is conductor's own agent image, built locally from
// images/agent (`make agent-image`): never pulled.
const DefaultAgentImage = "conductor-agent:latest"

// Labels every container and volume conductor creates carries.
const (
	LabelSandbox    = "conductor.sandbox"
	LabelDaemon     = "conductor.daemon"
	LabelIdentity   = "conductor.identity"
	LabelPersistent = "conductor.persistent"
	LabelSpec       = "conductor.spec"
	// LabelIdentityName is the identity as named (LabelIdentity is its
	// key, safe in a filter).
	LabelIdentityName = "conductor.identity-name"
	// LabelRole marks a one-shot helper container (the HOME seed).
	LabelRole = "conductor.role"
)

// Containers is the daemon's container backend: the engine, and every
// container it runs for agents.
type Containers struct {
	// Engine is the container engine; nil until Resolve finds it (on the
	// first launch, or Start).
	Engine Engine
	// Resolve finds the engine (and fills Helper/KeepID) when Engine is nil.
	Resolve func() (Engine, error)
	// Root holds the backend's host state (the helper binary dir).
	Root string
	// DaemonID labels this daemon's containers: it lists, reuses and
	// removes only its own.
	DaemonID string
	// Labels are added to every container and volume (tests mark theirs).
	Labels map[string]string
	// Helper is the host dir with conductor's Linux binary, bound
	// read-only ("" = HelperInImage: the image's own copy).
	Helper        string
	HelperInImage string
	UID, GID      int
	// KeepID maps the host user into the container (rootless podman).
	KeepID bool
	// IdleStop is the default idle time before a persistent container
	// stops.
	IdleStop time.Duration
	// Proxy mints the allowlist proxy's unix endpoint (the daemon's).
	Proxy func(allow []string) (sock, cred string, revoke func(), err error)
	// Defaults fill a policy's unset container fields (sandbox.container).
	Defaults Container
	Logf     func(string, ...any)

	mu    sync.Mutex
	boxes map[string]*box
	locks map[string]*sync.Mutex

	startMu  sync.Mutex
	started  bool
	startErr error
	startAt  time.Time
}

// startRetry is how soon a failed Start is tried again: an engine not up
// yet (Docker Desktop starting after the daemon at login) refuses launches
// until it is, never for the daemon's lifetime.
const startRetry = 5 * time.Second

// Start finds the engine and reconciles this daemon's containers. Success
// is kept; a failure is retried by a later call (at most every startRetry).
func (c *Containers) Start(ctx context.Context) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if c.started {
		return nil
	}
	if c.startErr != nil && time.Since(c.startAt) < startRetry {
		return c.startErr
	}
	c.startAt = time.Now()
	if c.Engine == nil {
		if c.Resolve == nil {
			c.startErr = errors.New("agentsandbox: no container engine")
			return c.startErr
		}
		e, err := c.Resolve()
		if err != nil {
			c.startErr = err
			return err
		}
		c.Engine = e
	}
	if err := c.Reconcile(ctx); err != nil {
		c.startErr = err
		return err
	}
	c.started, c.startErr = true, nil
	return nil
}

// box is one container this daemon runs.
type box struct {
	name, key, identity string
	persistent          bool
	spec                sandbox.AgentContainer
	mounts              []sandbox.ContainerMount

	mux     *sandbox.Mux
	leases  int
	idle    *time.Timer
	shared  map[string]*sharedListener // relays and the conductor socket, by in-container address
	idleFor time.Duration
}

type sharedListener struct {
	refs int
	stop func()
}

func (c *Containers) logf(f string, a ...any) {
	if c.Logf != nil {
		c.Logf(f, a...)
	}
}

func (c *Containers) keyLock(key string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.locks == nil {
		c.locks = map[string]*sync.Mutex{}
	}
	l := c.locks[key]
	if l == nil {
		l = &sync.Mutex{}
		c.locks[key] = l
	}
	return l
}

// labels for one identity's container or volume.
func (c *Containers) labels(identity, key string, persistent bool) map[string]string {
	l := map[string]string{
		LabelSandbox: "agent", LabelDaemon: c.DaemonID, LabelIdentity: key, LabelIdentityName: identity,
		LabelPersistent: fmt.Sprint(persistent),
	}
	for k, v := range c.Labels {
		l[k] = v
	}
	return l
}

// containerPrefix is this daemon's name prefix.
func (c *Containers) containerPrefix() string {
	d := c.DaemonID
	if len(d) > 8 {
		d = d[:8]
	}
	return "conductor-" + d + "-"
}

// VolumeName is an identity's persistent HOME volume.
func (c *Containers) VolumeName(identity string) string {
	return c.containerPrefix() + "home-" + IdentityKey(identity)
}

// merged is pol's container with the backend's defaults filled in.
func (c *Containers) merged(pc *Container) Container {
	m := *pc
	d := c.Defaults
	if m.Image == "" {
		m.Image = d.Image
	}
	if m.Image == "" {
		m.Image = DefaultAgentImage
	}
	if m.Memory == "" {
		m.Memory = d.Memory
	}
	if m.CPU == "" {
		m.CPU = d.CPU
	}
	if m.Pids == 0 {
		m.Pids = d.Pids
	}
	if m.IdleStop == "" {
		m.IdleStop = d.IdleStop
	}
	m.ReadOnlyRoot = m.ReadOnlyRoot || d.ReadOnlyRoot
	return m
}

// Prepare readies p's container and wires one launch into it: the
// container created or started, its mux up, this launch's egress proxy
// listener, relays, conductor socket and exposed ports. It fills
// p.Container.Session; release ends the launch's share (a non-persistent
// container is removed with its last launch). Fail-closed: any error means
// no launch.
func (c *Containers) Prepare(ctx context.Context, p *Policy) (release func(), err error) {
	if p.Container == nil {
		return nil, errors.New("agentsandbox: not a container policy")
	}
	if c == nil {
		return nil, errors.New("agentsandbox: the container backend is not available in this conductor — the agent is not launched")
	}
	if err := c.Start(ctx); err != nil || c.Engine == nil {
		return nil, fmt.Errorf("agentsandbox: the agent's container backend is unavailable (%v) — the agent is not launched", err)
	}
	if c.UID == 0 {
		return nil, errors.New("agentsandbox: an agent container runs as this daemon's user, and it is root — run conductor as a non-root user")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	want := c.merged(p.Container)
	identity, persistent := p.Identity, want.Persistent
	if want.Attach != "" {
		identity, persistent = want.Attach, true
	}
	key := IdentityKey(identity)
	if !persistent {
		key += "-" + nonce(6)
	}
	lock := c.keyLock(IdentityKey(identity))
	lock.Lock()
	defer lock.Unlock()

	var b *box
	if want.Attach != "" {
		b, err = c.attach(ctx, want.Attach, p)
	} else {
		b, err = c.ensure(ctx, identity, key, persistent, want, p)
	}
	if err != nil {
		return nil, err
	}
	sess, rel, err := c.wireLaunch(b, p)
	if err != nil {
		c.releaseBox(b)
		return nil, err
	}
	p.Container.Session = sess
	var once sync.Once
	return func() {
		once.Do(func() {
			rel()
			c.signal(sess, "KILL")
			lock.Lock()
			defer lock.Unlock()
			c.releaseBox(b)
		})
	}, nil
}

func nonce(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// spec builds the container p needs.
func (c *Containers) spec(ctx context.Context, identity string, persistent bool, want Container, p *Policy) (sandbox.AgentContainer, error) {
	envPath, imageID, err := c.inspectImage(ctx, want.Image)
	if err != nil {
		return sandbox.AgentContainer{}, err
	}
	home := sandbox.ContainerHome
	path := strings.Join([]string{home + "/.local/bin", home + "/bin", home + "/.npm-global/bin", home + "/go/bin", home + "/.cargo/bin"}, ":")
	if envPath == "" {
		envPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	if c.Helper != "" {
		// `conductor call` inside, by name (after the image's own: an image's
		// conductor, if it has one, is what its tools expect).
		envPath += ":" + sandbox.ContainerHelperDir
	}
	spec := sandbox.AgentContainer{
		Engine: c.Engine.Bin(), Image: want.Image, User: fmt.Sprintf("%d:%d", c.UID, c.GID), KeepID: c.KeepID,
		Helper: c.Helper, HelperInImage: c.HelperInImage, ReadOnlyRoot: want.ReadOnlyRoot,
		Memory: want.Memory, CPU: want.CPU, Pids: want.Pids,
		PullNever: want.Image == DefaultAgentImage,
		Env: []string{
			"HOME=" + home, "PATH=" + path + ":" + envPath, "TMPDIR=/tmp",
			"USER=agent", "LOGNAME=agent",
			"NPM_CONFIG_PREFIX=" + home + "/.npm-global", "PIP_USER=1", "PIP_BREAK_SYSTEM_PACKAGES=1",
		},
		Mounts: containerMounts(p),
	}
	if persistent {
		spec.HomeVolume = c.VolumeName(identity)
	} else {
		spec.HomeBind = p.Home
	}
	labels := c.labels(identity, IdentityKey(identity), persistent)
	sum := specHash(spec, imageID)
	labels[LabelSpec] = sum
	spec.Labels = labels
	return spec, nil
}

// containerMounts are p's paths as container mounts: the workspace and its
// git paths at their own paths, hidden ones as an empty tmpfs, and the
// login files at their place in HOME.
func containerMounts(p *Policy) []sandbox.ContainerMount {
	ms := []sandbox.ContainerMount{{Src: p.Workspace}}
	for _, q := range p.ReadWrite {
		if !within(q, p.Workspace) {
			ms = append(ms, sandbox.ContainerMount{Src: q})
		}
	}
	for _, q := range p.ReadOnly {
		ms = append(ms, sandbox.ContainerMount{Src: q, RO: true})
	}
	for _, q := range p.Hide {
		ms = append(ms, sandbox.ContainerMount{Dst: q, Tmpfs: true})
	}
	for _, q := range p.ReadWrite {
		if within(q, p.Workspace) {
			ms = append(ms, sandbox.ContainerMount{Src: q})
		}
	}
	for _, l := range p.Logins {
		rel, err := filepath.Rel(p.Home, l.Path)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		src := l.Src
		if r, err := filepath.EvalSymlinks(src); err == nil {
			src = r
		}
		if fi, err := os.Stat(src); err != nil || !fi.Mode().IsRegular() {
			continue // not signed in: nothing to share
		}
		ms = append(ms, sandbox.ContainerMount{Src: src, Dst: filepath.Join(sandbox.ContainerHome, rel)})
	}
	return ms
}

// specHash identifies a container's configuration: the same identity with
// another image (a rebuilt tag included), mounts or limits gets a new
// container; its HOME volume stays.
func specHash(s sandbox.AgentContainer, imageID string) string {
	s.Labels, s.Name = nil, ""
	b, _ := json.Marshal(struct {
		S       sandbox.AgentContainer
		ImageID string
	}{s, imageID})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// inspectImage returns the image's PATH and id, pulling an operator's image
// that is not here yet (conductor's own image is built, never pulled).
func (c *Containers) inspectImage(ctx context.Context, image string) (path, id string, err error) {
	inspect := func() ([]byte, error) {
		return c.Engine.Output(ctx, "image", "inspect", "--format", "{{json .Config.Env}}\t{{.Id}}", image)
	}
	out, err := inspect()
	if err != nil {
		if image == DefaultAgentImage {
			return "", "", fmt.Errorf("agentsandbox: conductor's agent image %s is not built on this box (run `make agent-image`, or name another image): %w", image, err)
		}
		if _, perr := c.Engine.Output(ctx, "pull", image); perr != nil {
			return "", "", fmt.Errorf("agentsandbox: agent image %s: %w", image, perr)
		}
		if out, err = inspect(); err != nil {
			return "", "", fmt.Errorf("agentsandbox: agent image %s: %w", image, err)
		}
	}
	envJSON, id, _ := strings.Cut(strings.TrimSpace(string(out)), "\t")
	var env []string
	_ = json.Unmarshal([]byte(envJSON), &env)
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	return path, id, nil
}

// engineContainer is one row of the engine's container list.
type engineContainer struct {
	Name, State, Spec, Identity, IdentityName string
	Persistent                                bool
}

// list returns this daemon's containers (optionally for one identity key).
func (c *Containers) list(ctx context.Context, key string) ([]engineContainer, error) {
	// The ids by label, then their details by `inspect`: the one template
	// docker and podman render alike (their `ps --format` fields differ).
	args := []string{"ps", "-a", "-q", "--no-trunc", "--filter", "label=" + LabelSandbox + "=agent", "--filter", "label=" + LabelDaemon + "=" + c.DaemonID}
	if key != "" {
		args = append(args, "--filter", "label="+LabelIdentity+"="+key)
	}
	for k, v := range c.Labels {
		args = append(args, "--filter", "label="+k+"="+v)
	}
	out, err := c.Engine.Output(ctx, args...)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil, nil
	}
	label := func(k string) string { return `{{index .Config.Labels "` + k + `"}}` }
	format := strings.Join([]string{"{{.Name}}", "{{.State.Status}}", label(LabelSpec), label(LabelIdentity), label(LabelPersistent), label(LabelIdentityName)}, "\t")
	out, err = c.Engine.Output(ctx, append([]string{"inspect", "--type", "container", "--format", format}, ids...)...)
	if err != nil {
		return nil, err
	}
	var rows []engineContainer
	// Per line, never the whole output: a trailing empty field (a label the
	// last container lacks) is a field, not whitespace to trim.
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 6 || f[0] == "" {
			continue
		}
		r := engineContainer{Name: strings.TrimPrefix(f[0], "/"), State: strings.ToLower(f[1]), Spec: f[2], Identity: f[3], Persistent: f[4] == "true", IdentityName: f[5]}
		if r.IdentityName == "" {
			r.IdentityName = r.Identity
		}
		rows = append(rows, r)
	}
	return rows, nil
}

// ensure finds, creates or starts identity's container and brings its mux
// up.
func (c *Containers) ensure(ctx context.Context, identity, key string, persistent bool, want Container, p *Policy) (*box, error) {
	spec, err := c.spec(ctx, identity, persistent, want, p)
	if err != nil {
		return nil, err
	}
	spec.Name = c.containerPrefix() + key + "-" + spec.Labels[LabelSpec][:8]
	idle := c.IdleStop
	if want.IdleStop != "" {
		if d, err := time.ParseDuration(want.IdleStop); err == nil && d > 0 {
			idle = d
		}
	}
	c.mu.Lock()
	b := c.boxes[spec.Name]
	c.mu.Unlock()
	if b == nil {
		b = &box{name: spec.Name}
	}
	// A box Reconcile adopted knows only its name: this is its spec.
	b.key, b.identity, b.persistent, b.spec, b.mounts, b.idleFor = key, identity, persistent, spec, spec.Mounts, idle

	rows, err := c.list(ctx, IdentityKey(identity))
	if err != nil {
		return nil, fmt.Errorf("agentsandbox: list containers: %w", err)
	}
	state := ""
	for _, r := range rows {
		if r.Name == spec.Name {
			state = r.State
			continue
		}
		// A persistent identity's container from another configuration
		// (image, mounts, limits): replaced once nothing runs in it.
		if persistent && r.Persistent && r.Spec != spec.Labels[LabelSpec] && !c.busy(r.Name) {
			c.logf("agent container %s: configuration changed; removing it (its HOME volume stays)", r.Name)
			c.forget(r.Name)
			_, _ = c.Engine.Output(ctx, "rm", "-f", r.Name)
		}
	}
	if state == "" {
		if persistent {
			if err := c.ensureVolume(ctx, identity, spec, p.Home); err != nil {
				return nil, err
			}
		}
		args, err := spec.CreateArgs()
		if err != nil {
			return nil, err
		}
		if _, err := c.Engine.Output(ctx, args[1:]...); err != nil {
			return nil, fmt.Errorf("agentsandbox: create the agent container: %w", err)
		}
		c.logf("agent container %s created for %s (persistent=%v, image %s)", spec.Name, identity, persistent, spec.Image)
		state = "created"
	}
	if state != "running" {
		if _, err := c.Engine.Output(ctx, "start", spec.Name); err != nil {
			return nil, fmt.Errorf("agentsandbox: start the agent container: %w", err)
		}
		if b.mux != nil {
			b.mux.Close()
			b.mux = nil
		}
	}
	if b.mux == nil || b.mux.Err() != nil {
		if err := c.startMux(b); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	if c.boxes == nil {
		c.boxes = map[string]*box{}
	}
	c.boxes[b.name] = b
	b.leases++
	if b.idle != nil {
		b.idle.Stop()
		b.idle = nil
	}
	c.mu.Unlock()
	return b, nil
}

// attach finds a running (or stopped) persistent container of identity to
// run p's launch in: it must show p's workspace.
func (c *Containers) attach(ctx context.Context, identity string, p *Policy) (*box, error) {
	key := IdentityKey(identity)
	c.mu.Lock()
	var b *box
	for _, x := range c.boxes {
		if x.key == key && x.persistent && x.spec.Name != "" {
			b = x
		}
	}
	c.mu.Unlock()
	if b == nil {
		return nil, fmt.Errorf("agentsandbox: no container of %s to attach to (it is created by that identity's own first launch)", identity)
	}
	if !mountsCover(b.mounts, p.Workspace) {
		return nil, fmt.Errorf("agentsandbox: %s's container does not show the workspace %s — refusing to attach", identity, p.Workspace)
	}
	rows, err := c.list(ctx, key)
	if err != nil {
		return nil, err
	}
	running := false
	for _, r := range rows {
		if r.Name == b.name {
			running = r.State == "running"
			if !running {
				if _, err := c.Engine.Output(ctx, "start", b.name); err != nil {
					return nil, fmt.Errorf("agentsandbox: start %s: %w", b.name, err)
				}
				running = true
			}
		}
	}
	if !running {
		return nil, fmt.Errorf("agentsandbox: %s's container is gone", identity)
	}
	if b.mux == nil || b.mux.Err() != nil {
		if err := c.startMux(b); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	b.leases++
	if b.idle != nil {
		b.idle.Stop()
		b.idle = nil
	}
	c.mu.Unlock()
	return b, nil
}

func mountsCover(ms []sandbox.ContainerMount, p string) bool {
	for _, m := range ms {
		if !m.Volume && !m.Tmpfs && m.Dst == "" && within(canon(p), canon(m.Src)) {
			return true
		}
	}
	return false
}

// busy reports a container this daemon has launches in.
func (c *Containers) busy(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.boxes[name]
	return b != nil && b.leases > 0
}

func (c *Containers) forget(name string) {
	c.mu.Lock()
	b := c.boxes[name]
	delete(c.boxes, name)
	c.mu.Unlock()
	if b != nil {
		if b.idle != nil {
			b.idle.Stop()
		}
		if b.mux != nil {
			b.mux.Close()
		}
	}
}

// ensureVolume creates identity's HOME volume if it has none, and seeds
// it from the prepared identity home — on every container creation, not
// only the volume's: the seed copies only what the volume lacks and gives
// it to the agent's user, so a seed a dead daemon left half done is
// finished here.
func (c *Containers) ensureVolume(ctx context.Context, identity string, spec sandbox.AgentContainer, seed string) error {
	vol := spec.HomeVolume
	created := false
	if _, err := c.Engine.Output(ctx, "volume", "inspect", vol); err != nil {
		args := []string{"volume", "create"}
		keys := make([]string, 0, len(spec.Labels))
		for k := range spec.Labels {
			if k != LabelSpec {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "--label", k+"="+spec.Labels[k])
		}
		if _, err := c.Engine.Output(ctx, append(args, vol)...); err != nil {
			return fmt.Errorf("agentsandbox: create HOME volume: %w", err)
		}
		created = true
		c.logf("agent container: HOME volume %s created for %s", vol, identity)
	}
	seedSpec := spec
	seedSpec.Name = c.containerPrefix() + IdentityKey(identity)
	// A one-shot: one a dead daemon left behind (created, never run) is
	// removed by the next one's Reconcile, as a per-launch container is.
	seedSpec.Labels = map[string]string{}
	for k, v := range spec.Labels {
		seedSpec.Labels[k] = v
	}
	seedSpec.Labels[LabelPersistent], seedSpec.Labels[LabelRole] = "false", "seed"
	sargs, err := seedSpec.SeedArgs(seed)
	if err == nil {
		_, err = c.Engine.Output(ctx, sargs[1:]...)
	}
	if err != nil {
		if created {
			_, _ = c.Engine.Output(ctx, "volume", "rm", vol)
		}
		return fmt.Errorf("agentsandbox: seed HOME volume %s: %w", vol, err)
	}
	return nil
}

// startMux execs the in-container mux and speaks to it.
func (c *Containers) startMux(b *box) error {
	out, in, wait, err := c.Engine.Start("exec", "-i", "--user", b.spec.User, b.name, b.spec.HelperPath(), "sandbox-net", "--mux")
	if err != nil {
		return fmt.Errorf("agentsandbox: start the container's mux: %w", err)
	}
	m, err := sandbox.NewHostMux(out, in, in)
	if err != nil {
		in.Close()
		go wait()
		return fmt.Errorf("agentsandbox: conductor's binary did not start in the container %s (is it a Linux build of this conductor, statically linked? set sandbox.container.helper): %w", b.name, err)
	}
	go func() {
		<-m.Done()
		in.Close()
		_ = wait()
	}()
	b.mux = m
	b.shared = map[string]*sharedListener{}
	return nil
}

// wireLaunch opens one launch's listeners on b's mux.
func (c *Containers) wireLaunch(b *box, p *Policy) (*ContainerSession, func(), error) {
	sess := &ContainerSession{Engine: c.Engine.Bin(), Name: b.name, User: b.spec.User, LaunchID: nonce(8), Helper: b.spec.HelperPath(), Exposed: map[string]string{}}
	var closers []func()
	release := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	fail := func(err error) (*ContainerSession, func(), error) {
		release()
		return nil, nil, err
	}
	if len(p.Egress) > 0 {
		if c.Proxy == nil {
			return fail(errors.New("agentsandbox: the container's egress needs conductor's proxy, which is not wired"))
		}
		sock, cred, revoke, err := c.Proxy(p.Egress)
		if err != nil {
			return fail(fmt.Errorf("agentsandbox: egress proxy: %w", err))
		}
		if revoke != nil {
			closers = append(closers, revoke)
		}
		addr, stop, err := b.mux.Listen("tcp", "127.0.0.1:0", func() (net.Conn, error) { return net.Dial("unix", sock) })
		if err != nil {
			return fail(err)
		}
		closers = append(closers, stop)
		sess.Env = append(sess.Env, sandbox.ProxyEnv(addr, cred)...)
	}
	for _, lb := range p.Loopback {
		target := lb
		if err := c.share(b, "tcp", lb, func() (net.Conn, error) { return net.Dial("tcp", target) }); err != nil {
			return fail(fmt.Errorf("agentsandbox: model relay %s: %w", lb, err))
		}
		closers = append(closers, func() { c.unshare(b, lb) })
	}
	if p.Socket != "" && p.Token != "" {
		sock := sandbox.ContainerSockDir + "/conductor.sock"
		host := p.Socket
		if err := c.share(b, "unix", sock, func() (net.Conn, error) { return net.Dial("unix", host) }); err != nil {
			return fail(fmt.Errorf("agentsandbox: conductor socket: %w", err))
		}
		closers = append(closers, func() { c.unshare(b, sock) })
		sess.Env = append(sess.Env, "CONDUCTOR_ENDPOINT=unix://"+sock, "CONDUCTOR_SKILL_TOKEN="+p.Token)
	}
	if len(p.Expose) > 0 {
		dir, err := SocketDir("cxe")
		if err != nil {
			return fail(err)
		}
		closers = append(closers, func() { _ = os.RemoveAll(dir) })
		for i, x := range p.Expose {
			hs := filepath.Join(dir, fmt.Sprintf("e%d.sock", i))
			ln, err := net.Listen("unix", hs)
			if err != nil {
				return fail(err)
			}
			_ = os.Chmod(hs, 0o600)
			closers = append(closers, func() { ln.Close() })
			go serveExposed(ln, b.mux, x)
			sess.Exposed[x] = hs
		}
	}
	return sess, release, nil
}

// share opens (or reuses) a container listener several launches use.
func (c *Containers) share(b *box, netw, addr string, dial func() (net.Conn, error)) error {
	c.mu.Lock()
	if s := b.shared[addr]; s != nil {
		s.refs++
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	_, stop, err := b.mux.Listen(netw, addr, dial)
	if err != nil {
		return err
	}
	c.mu.Lock()
	b.shared[addr] = &sharedListener{refs: 1, stop: stop}
	c.mu.Unlock()
	return nil
}

func (c *Containers) unshare(b *box, addr string) {
	c.mu.Lock()
	s := b.shared[addr]
	var stop func()
	if s != nil {
		s.refs--
		if s.refs <= 0 {
			delete(b.shared, addr)
			stop = s.stop
		}
	}
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// serveExposed pipes each host connection to addr in the container.
func serveExposed(ln net.Listener, m *sandbox.Mux, addr string) {
	for {
		hc, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			sc, err := m.Dial(addr)
			if err != nil {
				hc.Close()
				return
			}
			pipeConn(hc, sc)
		}()
	}
}

func pipeConn(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
	a.Close()
	b.Close()
}

// releaseBox ends one launch's share of b: a non-persistent container goes
// with its last launch, a persistent one stops when idle. Called with the
// identity's lock held.
func (c *Containers) releaseBox(b *box) {
	c.mu.Lock()
	b.leases--
	left := b.leases
	c.mu.Unlock()
	if left > 0 {
		return
	}
	if !b.persistent {
		c.forget(b.name)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := c.Engine.Output(ctx, "rm", "-f", b.name); err != nil {
			c.logf("agent container %s: remove: %v", b.name, err)
		}
		return
	}
	idle := b.idleFor
	if idle <= 0 {
		idle = 30 * time.Minute
	}
	c.mu.Lock()
	if b.idle != nil {
		b.idle.Stop()
	}
	b.idle = time.AfterFunc(idle, func() { c.stopIdle(b) })
	c.mu.Unlock()
}

// stopIdle stops b if nothing has run in it since its timer was set.
func (c *Containers) stopIdle(b *box) {
	lock := c.keyLock(IdentityKey(b.identity))
	lock.Lock()
	defer lock.Unlock()
	c.mu.Lock()
	if b.leases > 0 || c.boxes[b.name] != b {
		c.mu.Unlock()
		return
	}
	b.idle = nil
	m := b.mux
	b.mux = nil
	c.mu.Unlock()
	if m != nil {
		m.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := c.Engine.Output(ctx, "stop", "--time", "10", b.name); err != nil {
		c.logf("agent container %s: stop when idle: %v", b.name, err)
		return
	}
	c.logf("agent container %s stopped (idle)", b.name)
}

// signal sends sig to a launch's process group in its container.
func (c *Containers) signal(s *ContainerSession, sig string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = c.Engine.Output(ctx, "exec", "--user", s.User, s.Name, s.Helper, "sandbox-net", "--signal", s.LaunchID, sig)
}

// Archive removes identity's containers and HOME volume: the identity (a
// bot) is gone. Its running launches end with it.
func (c *Containers) Archive(ctx context.Context, identity string) (removed []string, err error) {
	if c == nil {
		return nil, errors.New("agentsandbox: no container backend")
	}
	if err := c.Start(ctx); err != nil {
		return nil, err
	}
	key := IdentityKey(identity)
	lock := c.keyLock(key)
	lock.Lock()
	defer lock.Unlock()
	rows, err := c.list(ctx, key)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		c.forget(r.Name)
		if _, err := c.Engine.Output(ctx, "rm", "-f", r.Name); err != nil {
			return removed, fmt.Errorf("agentsandbox: remove %s: %w", r.Name, err)
		}
		removed = append(removed, "container "+r.Name)
	}
	vol := c.VolumeName(identity)
	if _, err := c.Engine.Output(ctx, "volume", "inspect", vol); err == nil {
		if _, err := c.Engine.Output(ctx, "volume", "rm", vol); err != nil {
			return removed, fmt.Errorf("agentsandbox: remove volume %s: %w", vol, err)
		}
		removed = append(removed, "volume "+vol)
	}
	c.logf("agent container: archived %s (%s)", identity, strings.Join(removed, ", "))
	return removed, nil
}

// ContainerInfo is one of this daemon's agent containers.
type ContainerInfo struct {
	Name       string `json:"name"`
	Identity   string `json:"identity"`
	State      string `json:"state"`
	Persistent bool   `json:"persistent"`
	Launches   int    `json:"launches"`
}

// List reports this daemon's agent containers.
func (c *Containers) List(ctx context.Context) ([]ContainerInfo, error) {
	if c == nil {
		return nil, errors.New("agentsandbox: no container backend")
	}
	if err := c.Start(ctx); err != nil {
		return nil, err
	}
	rows, err := c.list(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]ContainerInfo, 0, len(rows))
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range rows {
		ci := ContainerInfo{Name: r.Name, Identity: r.IdentityName, State: r.State, Persistent: r.Persistent}
		if b := c.boxes[r.Name]; b != nil {
			ci.Launches = b.leases
			ci.Identity = b.identity
		}
		out = append(out, ci)
	}
	return out, nil
}

// Reconcile runs at daemon start: a non-persistent container left by a
// daemon that died is removed (its launch is gone), a running persistent
// one gets its idle timer.
func (c *Containers) Reconcile(ctx context.Context) error {
	if c == nil || c.Engine == nil {
		return nil
	}
	rows, err := c.list(ctx, "")
	if err != nil {
		return err
	}
	for _, r := range rows {
		switch {
		case !r.Persistent:
			c.logf("agent container %s: left by an earlier daemon; removing it", r.Name)
			_, _ = c.Engine.Output(ctx, "rm", "-f", r.Name)
		case r.State == "running":
			b := &box{name: r.Name, key: r.Identity, identity: r.IdentityName, persistent: true, idleFor: c.IdleStop}
			c.mu.Lock()
			if c.boxes == nil {
				c.boxes = map[string]*box{}
			}
			if c.boxes[r.Name] == nil {
				c.boxes[r.Name] = b
				idle := c.IdleStop
				if idle <= 0 {
					idle = 30 * time.Minute
				}
				b.idle = time.AfterFunc(idle, func() { c.stopIdle(b) })
			}
			c.mu.Unlock()
		}
	}
	return nil
}

// Close ends every mux (the containers keep running; a persistent one is
// picked up again by the next daemon, a non-persistent one removed by its
// Reconcile).
func (c *Containers) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	boxes := c.boxes
	c.boxes = nil
	c.mu.Unlock()
	for _, b := range boxes {
		if b.idle != nil {
			b.idle.Stop()
		}
		if b.mux != nil {
			b.mux.Close()
		}
	}
}

// ContainerFor is an isolation block's container backend (nil unless mode:
// container).
func ContainerFor(iso *config.IsolationConfig) *Container {
	if iso == nil || iso.Mode != "container" {
		return nil
	}
	c := &Container{Image: iso.ImageName(), Persistent: iso.IsPersistent()}
	if ci := iso.Container; ci != nil {
		c.Engine, c.ReadOnlyRoot, c.IdleStop, c.Attach = ci.Engine, ci.ReadOnly, ci.IdleStop, ci.Attach
	}
	if l := iso.Limits; l != nil {
		c.Memory, c.CPU, c.Pids = l.Memory, l.CPU, l.Pids
	}
	return c
}

// daemonContainers is the running daemon's container backend.
var daemonContainers atomic.Pointer[Containers]

// SetDaemonContainers records the daemon's container backend (nil: none).
func SetDaemonContainers(c *Containers) { daemonContainers.Store(c) }

// DaemonContainers is the running daemon's container backend, for callers
// outside the agent path that run something in a container (the browser):
// one backend, so its containers share this daemon's labels, Reconcile and
// Archive. nil when the agent sandbox is off.
func DaemonContainers() *Containers { return daemonContainers.Load() }

// LaunchSpec is a container launch for a caller outside the agent path —
// the browser's — with the agent container's guarantees: no network but
// Egress through conductor's proxy, no capabilities, non-root, only its
// mounts, a lease per launch.
type LaunchSpec struct {
	// Identity is whose container it is: with Container.Persistent one per
	// identity, reused by its launches.
	Identity string
	// Workspace is shown read-write at its own path (it must exist).
	Workspace string
	// Home is a per-launch container's HOME source ("" = a fresh directory
	// of the backend's); a persistent one's HOME is the identity's volume.
	Home      string
	Container Container
	// Egress (allowlist patterns), Loopback (host-loopback endpoints relayed
	// in at their address) and Expose (in-container addresses reached from
	// the host: Launch.Exposed names each one's unix socket) — as Policy.
	Egress, Loopback, Expose []string
	// Env are the process's variables (never a secret: use Inherited).
	Env []string
	// Argv runs in the container, by name on the image's PATH.
	Argv []string
	// Inherited is the caller's environment: the engine client's (where
	// the engine is) and any credential the process is given by name.
	Inherited []string
}

// Launch prepares s's container and returns the engine command that runs
// s.Argv in it. Run Launch.Argv with exactly Launch.Env; call Cleanup when
// it ends (it ends the launch's share of the container). Fail-closed.
func (c *Containers) Launch(ctx context.Context, s LaunchSpec) (*Launch, error) {
	if c == nil {
		return nil, errors.New("agentsandbox: no container backend")
	}
	if s.Identity == "" || len(s.Argv) == 0 {
		return nil, errors.New("agentsandbox: a container launch needs an identity and a command")
	}
	home, made := s.Home, ""
	if home == "" {
		if err := os.MkdirAll(c.Root, 0o700); err != nil {
			return nil, err
		}
		d, err := os.MkdirTemp(c.Root, "home-")
		if err != nil {
			return nil, err
		}
		home, made = d, d
	}
	drop := func() {
		if made != "" {
			_ = os.RemoveAll(made)
		}
	}
	cont := s.Container
	p := Policy{
		Identity: s.Identity, Workspace: canon(s.Workspace), Home: canon(home),
		Egress: s.Egress, Loopback: s.Loopback, Expose: s.Expose, Env: s.Env, Container: &cont,
	}
	release, err := c.Prepare(ctx, &p)
	if err != nil {
		drop()
		return nil, err
	}
	l, err := WrapContainer(p, s.Argv, Deps{Inherited: s.Inherited})
	if err != nil {
		release()
		drop()
		return nil, err
	}
	kill := l.Cleanup
	l.Cleanup = func() { kill(); release(); drop() }
	return l, nil
}
