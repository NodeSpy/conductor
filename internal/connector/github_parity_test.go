package connector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/pkg/githubkit/ghsource/ghsourcetest"
)

// GITHUB PARITY. One conformance table (pkg/githubkit/ghsource/ghsourcetest),
// three implementations' worth of runs:
//
//   - builtin: the bundled github connector, as `use: github` builds it;
//   - plugin:  the conductor-github plugin (test/plugins/conductor-github, the
//     same ghplugin handler the official build serves), spawned as a real
//     subprocess and driven through the daemon's own plugin path — describe,
//     registration in place of the bundled type, start_source carrying the
//     triggers, routed events, and the trusted_source grant;
//   - wire:    the same binary over the bare SDK protocol, which is what the
//     plugin repository runs against its own release build.
//
// Every case delivers signed webhooks to the implementation's real listener
// and sweeps against a mock API. A difference in which trigger fires, how
// often, or with which facts fails here.

func TestGithubConformanceBuiltin(t *testing.T) {
	ghsourcetest.Run(t, func(t *testing.T, c ghsourcetest.Case, env ghsourcetest.Env) ghsourcetest.Driver {
		return startParity(t, c, env, false)
	})
}

func TestGithubConformancePlugin(t *testing.T) {
	bin := githubPluginBin(t)
	ghsourcetest.Run(t, func(t *testing.T, c ghsourcetest.Case, env ghsourcetest.Env) ghsourcetest.Driver {
		cl := plugin.NewClient(plugin.Spec{Name: "github", Kind: plugin.KindConnector, Provides: "github", BinPath: bin, Local: true},
			plugin.Deps{})
		decl, err := cl.Describe(context.Background())
		if err != nil {
			t.Fatalf("describe: %v", err)
		}
		if _, err := RegisterExternalConnectorInPlaceOfBundled(cl, plugin.Spec{Name: "github", Kind: plugin.KindConnector,
			Provides: "github", BinPath: bin, Local: true}, decl); err != nil {
			t.Fatalf("register: %v", err)
		}
		d := startParity(t, c, env, true)
		d.cleanup = append(d.cleanup, func() {
			UnregisterExternalType("github")
			_ = cl.Close()
		})
		return d
	})
}

func TestGithubConformanceWire(t *testing.T) {
	ghsourcetest.Run(t, ghsourcetest.WireStarter(githubPluginBin(t)))
}

var (
	ghPluginOnce sync.Once
	ghPluginPath string
	ghPluginErr  string
)

// githubPluginBin builds the reference conductor-github plugin once.
func githubPluginBin(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	ghPluginOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ghplugin")
		if err != nil {
			ghPluginErr = err.Error()
			return
		}
		ghPluginPath = filepath.Join(dir, "conductor-github")
		cmd := exec.Command("go", "build", "-o", ghPluginPath, "github.com/NodeSpy/conductor/test/plugins/conductor-github")
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			ghPluginErr = err.Error() + "\n" + string(out)
		}
	})
	if ghPluginErr != "" {
		t.Fatalf("build conductor-github: %s", ghPluginErr)
	}
	return ghPluginPath
}

// parityDriver runs one built github connector's source.
type parityDriver struct {
	src     core.Integration
	cancel  context.CancelFunc
	done    chan struct{}
	cleanup []func()

	mu  sync.Mutex
	got []ghsourcetest.Got
}

func startParity(t *testing.T, c ghsourcetest.Case, env ghsourcetest.Env, trusted bool) *parityDriver {
	t.Helper()
	conn := c.Connection(env)
	conn["use"] = "github"
	if trusted {
		conn["trusted_source"] = true
	}
	var trigs []map[string]any
	for _, tr := range c.Triggers {
		m := map[string]any{"on": "gh." + tr.On, "name": tr.Name}
		if tr.Filter != nil {
			m["filter"] = tr.Filter
		}
		if tr.Options != nil {
			m["options"] = tr.Options
		}
		trigs = append(trigs, m)
	}
	doc, err := yaml.Marshal(map[string]any{"connectors": map[string]any{"gh": conn}, "triggers": trigs})
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(doc, &cfg); err != nil {
		t.Fatalf("config: %v\n%s", err, doc)
	}
	reg, err := Build(&cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	in, ok := reg.Get("gh")
	if !ok || in.DisabledReason != "" {
		t.Fatalf("gh not built: ok=%v reason=%q", ok, in.DisabledReason)
	}
	compiled := make([]CompiledTrigger, len(cfg.Triggers))
	for i, s := range cfg.Triggers {
		compiled[i] = CompiledTrigger{Index: i, Spec: s}
	}
	src, err := in.Impl.Source(compiled)
	if err != nil || src == nil {
		t.Fatalf("Source: %v (%T)", err, src)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &parityDriver{src: src, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		_ = src.Start(ctx, func(_ context.Context, tr core.Trigger) {
			d.mu.Lock()
			d.got = append(d.got, ghsourcetest.Got{
				Kind: tr.Kind, Trigger: tr.Variant, Repo: tr.Target.Repo, Number: tr.Target.Number,
				CatchUp: tr.CatchUp, TargetTrusted: tr.TargetTrusted, Context: tr.Context,
			})
			d.mu.Unlock()
		})
	}()
	return d
}

func (d *parityDriver) Nudge(t *testing.T) {
	t.Helper()
	sn, ok := d.src.(interface{ SweepNow() bool })
	if !ok || !sn.SweepNow() {
		t.Fatalf("%T has no sweep to nudge", d.src)
	}
}

func (d *parityDriver) Events() []ghsourcetest.Got {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]ghsourcetest.Got(nil), d.got...)
}

func (d *parityDriver) Close() {
	d.cancel()
	<-d.done
	for _, f := range d.cleanup {
		f()
	}
}
