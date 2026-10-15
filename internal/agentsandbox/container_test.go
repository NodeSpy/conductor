package agentsandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// fakeEngine keeps containers, volumes and images in memory, logs every
// command, and serves a real container mux in-process.
type fakeEngine struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	volumes    map[string]map[string]string
	images     map[string]string // image → its PATH
	cmds       [][]string
	fail       map[string]error // subcommand → error
	sockDir    string
}

type fakeContainer struct {
	state  string
	labels map[string]string
	args   []string
}

func newFakeEngine(t *testing.T) *fakeEngine {
	return &fakeEngine{
		containers: map[string]*fakeContainer{}, volumes: map[string]map[string]string{},
		images: map[string]string{"img:1": "/usr/bin:/bin", "img:2": "/usr/bin", DefaultAgentImage: "/usr/local/bin:/usr/bin"},
		fail:   map[string]error{}, sockDir: t.TempDir(),
	}
}

func (f *fakeEngine) Bin() string  { return "fake-engine" }
func (f *fakeEngine) Kind() string { return "docker" }

func (f *fakeEngine) log(args []string) {
	f.cmds = append(f.cmds, append([]string(nil), args...))
}

// ran reports the commands whose argv starts with prefix.
func (f *fakeEngine) ran(prefix ...string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.cmds {
		if len(c) >= len(prefix) && strings.Join(c[:len(prefix)], " ") == strings.Join(prefix, " ") {
			out = append(out, c)
		}
	}
	return out
}

func flagVals(args []string, flag string) []string {
	var out []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			out = append(out, args[i+1])
		}
	}
	return out
}

func labelsOf(args []string) map[string]string {
	l := map[string]string{}
	for _, kv := range flagVals(args, "--label") {
		k, v, _ := strings.Cut(kv, "=")
		l[k] = v
	}
	return l
}

func (f *fakeEngine) Output(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log(args)
	if err := f.fail[args[0]]; err != nil {
		return nil, err
	}
	switch args[0] {
	case "image":
		img := args[len(args)-1]
		p, ok := f.images[img]
		if !ok {
			return nil, fmt.Errorf("no such image %s", img)
		}
		return []byte(fmt.Sprintf(`["PATH=%s"]`+"\tsha256:%x\n", p, len(img))), nil
	case "pull":
		img := args[len(args)-1]
		if strings.HasPrefix(img, "missing") {
			return nil, errors.New("pull access denied")
		}
		f.images[img] = "/bin"
		return nil, nil
	case "create":
		name := flagVals(args, "--name")[0]
		if _, ok := f.containers[name]; ok {
			return nil, errors.New("name in use")
		}
		f.containers[name] = &fakeContainer{state: "created", labels: labelsOf(args), args: args}
		return []byte("id\n"), nil
	case "start":
		c := f.containers[args[1]]
		if c == nil {
			return nil, errors.New("no such container")
		}
		c.state = "running"
		return nil, nil
	case "stop":
		c := f.containers[args[len(args)-1]]
		if c == nil {
			return nil, errors.New("no such container")
		}
		c.state = "exited"
		return nil, nil
	case "rm":
		delete(f.containers, args[len(args)-1])
		return nil, nil
	case "ps":
		want := map[string]string{}
		for _, fl := range flagVals(args, "--filter") {
			k, v, _ := strings.Cut(strings.TrimPrefix(fl, "label="), "=")
			want[k] = v
		}
		var b strings.Builder
		for name, c := range f.containers {
			match := true
			for k, v := range want {
				match = match && c.labels[k] == v
			}
			if match {
				fmt.Fprintln(&b, "id-"+name)
			}
		}
		return []byte(b.String()), nil
	case "inspect":
		var b strings.Builder
		for _, a := range args {
			name, ok := strings.CutPrefix(a, "id-")
			if !ok {
				continue
			}
			if c := f.containers[name]; c != nil {
				fmt.Fprintf(&b, "/%s\t%s\t%s\t%s\t%s\t%s\n", name, c.state, c.labels[LabelSpec], c.labels[LabelIdentity], c.labels[LabelPersistent], c.labels[LabelIdentityName])
			}
		}
		return []byte(b.String()), nil
	case "volume":
		vol := args[len(args)-1]
		switch args[1] {
		case "inspect":
			if _, ok := f.volumes[vol]; !ok {
				return nil, errors.New("no such volume")
			}
		case "create":
			f.volumes[vol] = labelsOf(args)
		case "rm":
			delete(f.volumes, vol)
		}
		return nil, nil
	case "run", "exec":
		return nil, nil
	}
	return nil, fmt.Errorf("fake engine: %v", args)
}

func (f *fakeEngine) Start(args ...string) (io.Reader, io.WriteCloser, func() error, error) {
	f.mu.Lock()
	f.log(args)
	name := args[4] // exec -i --user U NAME …
	c := f.containers[name]
	running := c != nil && c.state == "running"
	f.mu.Unlock()
	if !running {
		return nil, nil, nil, fmt.Errorf("container %s is not running", name)
	}
	h2cR, h2cW := io.Pipe()
	c2hR, c2hW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- sandbox.ServeContainerMux(h2cR, c2hW, f.sockDir)
		c2hW.Close()
	}()
	return c2hR, h2cW, func() error { return <-done }, nil
}

func fakeContainers(t *testing.T, e *fakeEngine) *Containers {
	t.Helper()
	c := &Containers{
		Engine: e, Root: t.TempDir(), DaemonID: "d1", Labels: map[string]string{"conductor.test": "unit"},
		Helper: t.TempDir(), UID: 1000, GID: 1000, IdleStop: time.Hour, Logf: t.Logf,
		Defaults: Container{Image: "img:1"},
	}
	t.Cleanup(c.Close)
	return c
}

func fakePolicy(t *testing.T, identity string, persistent bool) Policy {
	t.Helper()
	root := t.TempDir()
	ws, home := filepath.Join(root, "ws"), filepath.Join(root, "home")
	_ = os.MkdirAll(ws, 0o755)
	_ = os.MkdirAll(home, 0o755)
	return Policy{Identity: identity, Workspace: canon(ws), Home: canon(home), Container: &Container{Persistent: persistent}}
}

// A per-launch container is created, started, and removed with its launch.
func TestContainersPerLaunch(t *testing.T) {
	e := newFakeEngine(t)
	c := fakeContainers(t, e)
	p := fakePolicy(t, "agent:a@o/r", false)
	rel, err := c.Prepare(context.Background(), &p)
	if err != nil {
		t.Fatal(err)
	}
	s := p.Container.Session
	if s == nil || s.Name == "" || s.User != "1000:1000" || s.Helper != sandbox.ContainerHelperDir+"/conductor" || s.LaunchID == "" {
		t.Fatalf("session %+v", s)
	}
	creates := e.ran("create")
	if len(creates) != 1 {
		t.Fatalf("creates %d", len(creates))
	}
	args := creates[0]
	if err := sandbox.CheckContainerArgs(append([]string{"x"}, args...)); err != nil {
		t.Errorf("the create argv breaks the invariant: %v", err)
	}
	l := labelsOf(args)
	if l[LabelDaemon] != "d1" || l["conductor.test"] != "unit" || l[LabelPersistent] != "false" || l[LabelSandbox] != "agent" || l[LabelIdentity] != IdentityKey("agent:a@o/r") {
		t.Errorf("labels %v", l)
	}
	if got := flagVals(args, "--mount"); !strings.Contains(strings.Join(got, " "), "type=bind,src="+p.Home+",dst="+sandbox.ContainerHome) {
		t.Errorf("a per-launch container's HOME is the identity home: %v", got)
	}
	if len(e.ran("volume", "create")) != 0 {
		t.Error("a per-launch container made a volume")
	}
	if len(e.ran("start")) != 1 || len(e.ran("exec", "-i")) != 1 {
		t.Error("not started, or no mux")
	}
	rel()
	rel() // idempotent
	if len(e.containers) != 0 {
		t.Errorf("the per-launch container outlived its launch: %v", e.containers)
	}
	if len(e.ran("rm", "-f")) != 1 {
		t.Errorf("rm ran %d times", len(e.ran("rm", "-f")))
	}
	if sig := e.ran("exec", "--user"); len(sig) != 1 || strings.Join(sig[0][len(sig[0])-3:], " ") != "--signal "+s.LaunchID+" KILL" {
		t.Errorf("the launch's group was not killed: %v", sig)
	}
	// A second launch of the same identity: a container of its own.
	p2 := fakePolicy(t, "agent:a@o/r", false)
	rel2, err := c.Prepare(context.Background(), &p2)
	if err != nil {
		t.Fatal(err)
	}
	defer rel2()
	if p2.Container.Session.Name == s.Name {
		t.Error("two per-launch containers share a name")
	}
}

// A persistent identity: one container and one HOME volume (seeded once),
// reused by every launch; stopped when idle.
func TestContainersPersistentReuse(t *testing.T) {
	e := newFakeEngine(t)
	c := fakeContainers(t, e)
	c.IdleStop = 50 * time.Millisecond
	p := fakePolicy(t, "member:alice", true)
	rel1, err := c.Prepare(context.Background(), &p)
	if err != nil {
		t.Fatal(err)
	}
	name := p.Container.Session.Name
	p2 := p
	p2.Container = &Container{Persistent: true}
	rel2, err := c.Prepare(context.Background(), &p2)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Container.Session.Name != name || len(e.ran("create")) != 1 {
		t.Fatalf("a second launch made another container (%s vs %s, %d creates)", p2.Container.Session.Name, name, len(e.ran("create")))
	}
	if len(e.ran("volume", "create")) != 1 || len(e.ran("run")) != 1 {
		t.Errorf("volume created %d, seeded %d — want once each", len(e.ran("volume", "create")), len(e.ran("run")))
	}
	vol := c.VolumeName("member:alice")
	if l := e.volumes[vol]; l[LabelDaemon] != "d1" || l["conductor.test"] != "unit" {
		t.Errorf("volume labels %v", l)
	}
	seed := e.ran("run")[0]
	if err := checkSeed(seed); err != nil {
		t.Error(err)
	}
	if l := labelsOf(seed); l[LabelPersistent] != "false" || l[LabelRole] != "seed" || l[LabelDaemon] != "d1" || l["conductor.test"] != "unit" {
		t.Errorf("seed labels %v: a seed a dead daemon leaves must be reconciled away", l)
	}
	if got := flagVals(e.ran("create")[0], "--mount"); got[0] != "type=volume,src="+vol+",dst="+sandbox.ContainerHome {
		t.Errorf("HOME mount %v", got)
	}
	if p2.Container.Session.LaunchID == p.Container.Session.LaunchID {
		t.Error("two launches share a launch id")
	}
	rel1()
	time.Sleep(150 * time.Millisecond)
	if e.containers[name].state != "running" {
		t.Fatal("stopped while a launch still runs")
	}
	rel2()
	deadline := time.Now().Add(3 * time.Second)
	for e.stateOf(name) != "exited" {
		if time.Now().After(deadline) {
			t.Fatalf("an idle persistent container was not stopped (%s)", e.stateOf(name))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := e.volumes[vol]; !ok {
		t.Error("the HOME volume went with the stop")
	}
	// The next launch starts it again, same container.
	p3 := fakePolicy(t, "member:alice", true)
	p3.Workspace, p3.Home = p.Workspace, p.Home
	rel3, err := c.Prepare(context.Background(), &p3)
	if err != nil {
		t.Fatal(err)
	}
	defer rel3()
	if p3.Container.Session.Name != name || e.stateOf(name) != "running" || len(e.ran("create")) != 1 {
		t.Error("a stopped container was not restarted")
	}
	if len(e.ran("run")) != 1 {
		t.Error("a restart re-seeded (only a container's creation seeds)")
	}
}

func (f *fakeEngine) stateOf(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.containers[name]; c != nil {
		return c.state
	}
	return ""
}

// checkSeed is the seed run's own invariant.
func checkSeed(args []string) error {
	if args[0] != "run" || flagVals(args, "--user")[0] != "0:0" || flagVals(args, "--network")[0] != "none" {
		return fmt.Errorf("seed argv %v", args)
	}
	if caps := strings.Join(flagVals(args, "--cap-add"), ","); caps != "CHOWN,DAC_OVERRIDE,FOWNER" {
		return fmt.Errorf("seed caps %s", caps)
	}
	return nil
}

// A changed configuration (another image) gets a new container — the
// idle old one removed, the HOME volume kept; a busy one is left alone.
func TestContainersSpecChange(t *testing.T) {
	e := newFakeEngine(t)
	c := fakeContainers(t, e)
	p := fakePolicy(t, "member:bob", true)
	rel, err := c.Prepare(context.Background(), &p)
	if err != nil {
		t.Fatal(err)
	}
	old := p.Container.Session.Name
	// Busy: a launch in a new configuration leaves the old container be.
	p2 := p
	p2.Container = &Container{Persistent: true, Image: "img:2"}
	rel2, err := c.Prepare(context.Background(), &p2)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Container.Session.Name == old {
		t.Fatal("another image reused the old container")
	}
	if e.stateOf(old) == "" {
		t.Fatal("a busy container was removed")
	}
	rel()
	rel2()
	// Idle now: the next launch in the new configuration removes the old.
	p3 := p
	p3.Container = &Container{Persistent: true, Image: "img:2"}
	rel3, err := c.Prepare(context.Background(), &p3)
	if err != nil {
		t.Fatal(err)
	}
	defer rel3()
	if e.stateOf(old) != "" {
		t.Error("an idle container of the old configuration survived")
	}
	if len(e.ran("volume", "create")) != 1 {
		t.Error("the HOME volume was recreated")
	}
	if len(e.ran("run")) != 2 {
		t.Errorf("a new container's HOME was not seeded (%d seeds for 2 creations)", len(e.ran("run")))
	}
}

// Archive removes an identity's containers and volume — only its own, and
// only this daemon's.
func TestContainersArchive(t *testing.T) {
	e := newFakeEngine(t)
	c := fakeContainers(t, e)
	pa := fakePolicy(t, "member:alice", true)
	ra, err := c.Prepare(context.Background(), &pa)
	if err != nil {
		t.Fatal(err)
	}
	ra()
	pb := fakePolicy(t, "member:bob", true)
	rb, err := c.Prepare(context.Background(), &pb)
	if err != nil {
		t.Fatal(err)
	}
	defer rb()
	// Another daemon's container for the same identity key, and a stranger.
	e.containers["conductor-other-alice"] = &fakeContainer{state: "running", labels: map[string]string{LabelSandbox: "agent", LabelDaemon: "d2", LabelIdentity: IdentityKey("member:alice"), "conductor.test": "unit"}}
	e.containers["someone-elses"] = &fakeContainer{state: "running", labels: map[string]string{}}
	removed, err := c.Archive(context.Background(), "member:alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 || !strings.HasPrefix(removed[0], "container ") || removed[1] != "volume "+c.VolumeName("member:alice") {
		t.Errorf("removed %v", removed)
	}
	if e.stateOf(pa.Container.Session.Name) != "" {
		t.Error("alice's container survived")
	}
	if _, ok := e.volumes[c.VolumeName("member:alice")]; ok {
		t.Error("alice's volume survived")
	}
	if e.stateOf(pb.Container.Session.Name) != "running" || e.volumes[c.VolumeName("member:bob")] == nil {
		t.Error("bob's container or volume went with alice's archive")
	}
	if e.stateOf("conductor-other-alice") == "" || e.stateOf("someone-elses") == "" {
		t.Error("archive touched a container this daemon did not create")
	}
	// Nothing left: nothing removed, no error.
	if removed, err := c.Archive(context.Background(), "member:alice"); err != nil || len(removed) != 0 {
		t.Errorf("second archive: %v %v", removed, err)
	}
}

// At start a daemon removes its own leftover per-launch containers and
// gives its running persistent ones an idle timer — and touches no other.
func TestContainersReconcile(t *testing.T) {
	e := newFakeEngine(t)
	mine := map[string]string{LabelSandbox: "agent", LabelDaemon: "d1", "conductor.test": "unit"}
	with := func(extra map[string]string) map[string]string {
		l := map[string]string{}
		for k, v := range mine {
			l[k] = v
		}
		for k, v := range extra {
			l[k] = v
		}
		return l
	}
	e.containers["stale"] = &fakeContainer{state: "running", labels: with(map[string]string{LabelPersistent: "false", LabelIdentity: "a"})}
	e.containers["member"] = &fakeContainer{state: "running", labels: with(map[string]string{LabelPersistent: "true", LabelIdentity: "m"})}
	e.containers["member-stopped"] = &fakeContainer{state: "exited", labels: with(map[string]string{LabelPersistent: "true", LabelIdentity: "n"})}
	e.containers["other-daemon"] = &fakeContainer{state: "running", labels: map[string]string{LabelSandbox: "agent", LabelDaemon: "d2", LabelPersistent: "false", "conductor.test": "unit"}}
	c := fakeContainers(t, e)
	c.IdleStop = 50 * time.Millisecond
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.stateOf("stale") != "" {
		t.Error("a dead daemon's per-launch container survived")
	}
	if e.stateOf("other-daemon") == "" || e.stateOf("member-stopped") != "exited" {
		t.Error("reconcile touched what it should not")
	}
	deadline := time.Now().Add(3 * time.Second)
	for e.stateOf("member") != "exited" {
		if time.Now().After(deadline) {
			t.Fatal("a running persistent container got no idle stop")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// No engine, root, an unbuilt image: no launch.
func TestContainersRefuse(t *testing.T) {
	c := &Containers{Resolve: func() (Engine, error) { return nil, errors.New("no container engine") }, UID: 1000}
	p := fakePolicy(t, "a", false)
	if _, err := c.Prepare(context.Background(), &p); err == nil || !strings.Contains(err.Error(), "not launched") {
		t.Errorf("no engine: %v", err)
	}
	var nilc *Containers
	if _, err := nilc.Prepare(context.Background(), &p); err == nil {
		t.Error("nil backend prepared")
	}
	e := newFakeEngine(t)
	c = fakeContainers(t, e)
	c.UID = 0
	if _, err := c.Prepare(context.Background(), &p); err == nil || !strings.Contains(err.Error(), "root") {
		t.Errorf("root: %v", err)
	}
	c = fakeContainers(t, e)
	np := p
	np.Container = nil
	if _, err := c.Prepare(context.Background(), &np); err == nil {
		t.Error("a non-container policy prepared")
	}
	c.Defaults.Image = ""
	delete(e.images, DefaultAgentImage)
	p.Container = &Container{}
	if _, err := c.Prepare(context.Background(), &p); err == nil || !strings.Contains(err.Error(), "make agent-image") {
		t.Errorf("unbuilt default image: %v", err)
	}
	if len(e.ran("pull")) != 0 {
		t.Error("conductor's own image was pulled")
	}
	p.Container = &Container{Image: "missing/img"}
	if _, err := c.Prepare(context.Background(), &p); err == nil {
		t.Error("an unpullable image prepared")
	}
	if len(e.ran("pull")) != 1 {
		t.Error("an operator's image was not pulled")
	}
	p.Container = &Container{Image: "img:1"}
	bad := p
	bad.Workspace = "/nonexistent-ws"
	if _, err := c.Prepare(context.Background(), &bad); err == nil {
		t.Error("an invalid policy prepared")
	}
	e.fail["create"] = errors.New("engine refused")
	if _, err := c.Prepare(context.Background(), &p); err == nil {
		t.Error("a failed create prepared")
	}
	if len(e.ran("start")) != 0 {
		t.Error("started after a failed create")
	}
}

// A launch's egress: its own listener in the container, its own proxy
// credential, retired with it; a host-loopback model relay; an exposed
// server reached through a host socket.
func TestContainersLaunchWiring(t *testing.T) {
	e := newFakeEngine(t)
	c := fakeContainers(t, e)
	var allowSeen []string
	revoked := 0
	proxySock := filepath.Join(t.TempDir(), "p.sock")
	pl, err := net.Listen("unix", proxySock)
	if err != nil {
		t.Fatal(err)
	}
	defer pl.Close()
	go func() {
		for {
			cn, err := pl.Accept()
			if err != nil {
				return
			}
			fmt.Fprintln(cn, "PROXY")
			cn.Close()
		}
	}()
	c.Proxy = func(allow []string) (string, string, func(), error) {
		allowSeen = allow
		return proxySock, "cred1", func() { revoked++ }, nil
	}
	srv := serveLine(t, "tcp", "127.0.0.1:0", "SERVER").Addr().String()
	p := fakePolicy(t, "agent:w", false)
	p.Egress = []string{"api.example.com"}
	p.Expose = []string{srv}
	rel, err := c.Prepare(context.Background(), &p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(allowSeen, ",") != "api.example.com" {
		t.Errorf("proxy allowlist %v", allowSeen)
	}
	env := strings.Join(p.Container.Session.Env, "\n")
	if !strings.Contains(env, "HTTPS_PROXY=http://conductor:cred1@127.0.0.1:") {
		t.Errorf("session env %s", env)
	}
	// The listener the container got (here: in-process) reaches the proxy.
	var addr string
	for _, kv := range p.Container.Session.Env {
		if v, ok := strings.CutPrefix(kv, "HTTP_PROXY=http://conductor:cred1@"); ok {
			addr = v
		}
	}
	if got := readLine(t, "tcp", addr); got != "PROXY" {
		t.Errorf("egress listener reached %q", got)
	}
	hs := p.Container.Session.Exposed[srv]
	if hs == "" {
		t.Fatal("the exposed server has no host socket")
	}
	if got := readLine(t, "unix", hs); got != "SERVER" {
		t.Errorf("exposed server %q", got)
	}
	rel()
	if revoked != 1 {
		t.Errorf("the proxy credential was revoked %d times", revoked)
	}
	if _, err := os.Stat(hs); err == nil {
		t.Error("the exposed socket outlived the launch")
	}
	// A relay that cannot be wired (here the fake container shares this
	// network, so the model's own address is taken; the real relay is
	// TestContainerBoundary's): no launch, the credential retired, the
	// container gone.
	model := serveLine(t, "tcp", "127.0.0.1:0", "MODEL").Addr().String()
	pr := fakePolicy(t, "agent:r", false)
	pr.Egress = []string{"api.example.com"}
	pr.Loopback = []string{model}
	if _, err := c.Prepare(context.Background(), &pr); err == nil || !strings.Contains(err.Error(), "model relay") {
		t.Errorf("an unwireable relay: %v", err)
	}
	if revoked != 2 || len(e.containers) != 0 {
		t.Errorf("a failed relay left its credential (%d revoked) or container (%v)", revoked, e.containers)
	}
	// No proxy wired, egress wanted: no launch.
	c.Proxy = nil
	p2 := fakePolicy(t, "agent:w2", false)
	p2.Egress = []string{"x.com"}
	if _, err := c.Prepare(context.Background(), &p2); err == nil {
		t.Error("prepared egress with no proxy")
	}
	if len(e.containers) != 0 {
		t.Errorf("a failed launch left its container: %v", e.containers)
	}
}

func readLine(t *testing.T, netw, addr string) string {
	t.Helper()
	cn, err := net.DialTimeout(netw, addr, 5*time.Second)
	if err != nil {
		return "dial: " + err.Error()
	}
	defer cn.Close()
	_ = cn.SetReadDeadline(time.Now().Add(5 * time.Second))
	b, _ := io.ReadAll(cn)
	return strings.TrimSpace(string(b))
}

// A worker attaches to a member's container only when it exists and shows
// the worker's workspace.
func TestContainersAttach(t *testing.T) {
	e := newFakeEngine(t)
	c := fakeContainers(t, e)
	worker := fakePolicy(t, "agent:w", false)
	worker.Container = &Container{Attach: "member:m"}
	if _, err := c.Prepare(context.Background(), &worker); err == nil || !strings.Contains(err.Error(), "no container of member:m") {
		t.Errorf("attach to nothing: %v", err)
	}
	member := fakePolicy(t, "member:m", true)
	rel, err := c.Prepare(context.Background(), &member)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	if _, err := c.Prepare(context.Background(), &worker); err == nil || !strings.Contains(err.Error(), "does not show the workspace") {
		t.Errorf("attach outside the member's workspace: %v", err)
	}
	inside := filepath.Join(member.Workspace, "sub")
	_ = os.MkdirAll(inside, 0o755)
	worker.Workspace = inside
	worker.Container = &Container{Attach: "member:m"}
	wrel, err := c.Prepare(context.Background(), &worker)
	if err != nil {
		t.Fatal(err)
	}
	if worker.Container.Session.Name != member.Container.Session.Name || len(e.ran("create")) != 1 {
		t.Error("the worker did not run in the member's container")
	}
	wrel()
	if e.stateOf(member.Container.Session.Name) != "running" {
		t.Error("the worker's end stopped the member's container")
	}
}

// List shows this daemon's containers with their launches.
func TestContainersList(t *testing.T) {
	e := newFakeEngine(t)
	c := fakeContainers(t, e)
	p := fakePolicy(t, "member:l", true)
	rel, err := c.Prepare(context.Background(), &p)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	e.containers["foreign"] = &fakeContainer{state: "running", labels: map[string]string{LabelSandbox: "agent", LabelDaemon: "zz"}}
	list, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Identity != "member:l" || !list[0].Persistent || list[0].Launches != 1 || list[0].State != "running" {
		t.Errorf("list %+v", list)
	}
}

// An isolation block's container backend.
func TestContainerFor(t *testing.T) {
	if ContainerFor(nil) != nil || ContainerFor(&config.IsolationConfig{Mode: "namespace"}) != nil {
		t.Error("not a container")
	}
	yes := true
	got := ContainerFor(&config.IsolationConfig{Mode: "container", Image: "i:1", Persistent: &yes,
		Container: &config.ContainerIsolation{Engine: "podman", ReadOnly: true, IdleStop: "5m", Attach: "m"},
		Limits:    &config.IsolationLimits{Memory: "1g", CPU: "2", Pids: 64}})
	want := Container{Image: "i:1", Persistent: true, Engine: "podman", ReadOnlyRoot: true, IdleStop: "5m", Attach: "m", Memory: "1g", CPU: "2", Pids: 64}
	if *got != want {
		t.Errorf("%+v, want %+v", *got, want)
	}
	got = ContainerFor(&config.IsolationConfig{Mode: "container", Container: &config.ContainerIsolation{Image: "c:2"}})
	if got.Image != "c:2" || got.Persistent {
		t.Errorf("%+v", *got)
	}
}

// An engine that is not up yet refuses launches until it is — then they
// run; the failure is not kept for the daemon's lifetime.
func TestContainersStartRecovers(t *testing.T) {
	e := newFakeEngine(t)
	e.fail["ps"] = errors.New("cannot connect to the engine")
	c := fakeContainers(t, e)
	p := fakePolicy(t, "agent:late", false)
	if _, err := c.Prepare(context.Background(), &p); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("engine down: %v", err)
	}
	// Retried at most every startRetry, not on every launch.
	if _, err := c.Prepare(context.Background(), &p); err == nil {
		t.Fatal("prepared while the engine was down")
	}
	if n := len(e.ran("ps")); n != 1 {
		t.Errorf("the engine was asked %d times within startRetry", n)
	}
	e.mu.Lock()
	delete(e.fail, "ps")
	e.mu.Unlock()
	c.startMu.Lock()
	c.startAt = time.Now().Add(-startRetry)
	c.startMu.Unlock()
	p = fakePolicy(t, "agent:late", false)
	rel, err := c.Prepare(context.Background(), &p)
	if err != nil {
		t.Fatalf("the engine is up: %v", err)
	}
	rel()
	// Started: not reconciled again.
	before := len(e.ran("ps"))
	p = fakePolicy(t, "agent:late", false)
	rel, err = c.Prepare(context.Background(), &p)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if len(e.ran("ps"))-before != 1 { // the launch's own list, no second reconcile
		t.Errorf("reconciled again after a successful start (%d lists)", len(e.ran("ps"))-before)
	}
}
