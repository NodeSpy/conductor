package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/hosts"
)

// projectFixture points HOME at a temp dir and returns a dispatcher driving the
// fake paseo, plus the conductor checkout dir under that HOME.
func projectFixture(t *testing.T) (d *Dispatcher, dir, home string) {
	t.Helper()
	bin, dir := fakePaseoDir(t)
	home = t.TempDir()
	t.Setenv("HOME", home)
	return &Dispatcher{PaseoBin: bin}, dir, home
}

func prune(t *testing.T, d *Dispatcher) []string {
	t.Helper()
	removed, err := d.PruneProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return removed
}

// The pile-up this fixes: conductor cuts a worktree from its own base checkout,
// paseo registers that checkout as a project, and the project outlives every
// workspace in it. Once idle it must go — but only on the SECOND idle sighting,
// so a project isn't yanked between one step's archive and the next step's
// worktree.
func TestPruneProjectsRemovesIdleConductorCheckoutOnSecondSweep(t *testing.T) {
	d, dir, home := projectFixture(t)
	checkout := filepath.Join(home, ".conductor", "checkouts", "RosterStream")
	put(t, dir, "projects.json", `[{"projectId":"prj_c","name":"RosterStream","path":"`+checkout+`"}]`)

	if got := prune(t, d); len(got) != 0 {
		t.Fatalf("first idle sighting must not remove anything, removed %v", got)
	}
	if strings.Contains(callsLog(t, dir), "project delete") {
		t.Fatalf("no delete on the first sweep:\n%s", callsLog(t, dir))
	}
	if got := prune(t, d); len(got) != 1 || got[0] != checkout {
		t.Fatalf("second idle sighting should remove %s, removed %v", checkout, got)
	}
	if !strings.Contains(callsLog(t, dir), "project delete prj_c") {
		t.Fatalf("expected `project delete prj_c`:\n%s", callsLog(t, dir))
	}
}

// A project that comes back into use between sweeps restarts its grace period.
func TestPruneProjectsBusyBetweenSweepsResetsGrace(t *testing.T) {
	d, dir, home := projectFixture(t)
	checkout := filepath.Join(home, ".conductor", "checkouts", "w")
	put(t, dir, "projects.json", `[{"projectId":"prj_c","name":"w","path":"`+checkout+`"}]`)

	prune(t, d) // idle
	put(t, dir, "workspaces.json", `[{"workspaceId":"wks_1","project":"w","isolation":"worktree","cwd":"/home/u/.paseo/worktrees/x/y"}]`)
	prune(t, d) // busy
	put(t, dir, "workspaces.json", `[]`)
	if got := prune(t, d); len(got) != 0 {
		t.Fatalf("idle again after being busy is a FIRST sighting, removed %v", got)
	}
	if got := prune(t, d); len(got) != 1 {
		t.Fatalf("second consecutive idle sighting should remove it, removed %v", got)
	}
}

// What keeps a project: an active workspace naming it (a worktree, whose cwd is
// under ~/.paseo, not the project), or one sitting inside its directory (a run
// workspace, whose cwd IS the project path). And a project that isn't
// conductor's — yours, wherever it lives — is never considered at all.
func TestPruneProjectsKeepsBusyAndForeignProjects(t *testing.T) {
	d, dir, home := projectFixture(t)
	busyByName := filepath.Join(home, ".conductor", "checkouts", "RosterStream")
	busyByCwd := filepath.Join(home, ".conductor", "runs", "cron-7-abc")
	put(t, dir, "projects.json", `[
		{"projectId":"prj_name","name":"RosterStream","path":"`+busyByName+`"},
		{"projectId":"prj_cwd","name":"cron-7-abc","path":"`+busyByCwd+`"},
		{"projectId":"prj_mine","name":"ednition/rosterstream","path":"`+filepath.Join(home, "Projects", "rosterstream")+`"},
		{"projectId":"prj_home","name":"u","path":"`+home+`"},
		{"projectId":"prj_root","name":"conductor","path":"`+filepath.Join(home, ".conductor")+`"}
	]`)
	put(t, dir, "workspaces.json", `[
		{"workspaceId":"wks_wt","project":"rosterstream","isolation":"worktree","cwd":"/home/u/.paseo/worktrees/a/b"},
		{"workspaceId":"wks_run","project":"something-else","isolation":"local","cwd":"`+busyByCwd+`"}
	]`)

	prune(t, d)
	if got := prune(t, d); len(got) != 0 {
		t.Fatalf("busy and foreign projects must all survive, removed %v", got)
	}
	if strings.Contains(callsLog(t, dir), "project delete") {
		t.Fatalf("nothing may be deleted:\n%s", callsLog(t, dir))
	}
}

// Without the full list of active workspaces nothing can be proven idle, and a
// wrong "idle" would have paseo archive live work — so a listing failure
// removes nothing and also must not count as an idle sighting.
func TestPruneProjectsWorkspaceListFailureRemovesNothing(t *testing.T) {
	d, dir, home := projectFixture(t)
	checkout := filepath.Join(home, ".conductor", "checkouts", "w")
	put(t, dir, "projects.json", `[{"projectId":"prj_c","name":"w","path":"`+checkout+`"}]`)
	prune(t, d) // idle sighting before the failures — they must void it
	put(t, dir, "workspaces.json", `not json`)

	for i := 0; i < 2; i++ {
		if _, err := d.PruneProjects(context.Background()); err == nil {
			t.Fatal("expected the workspace listing error to surface")
		}
	}
	if strings.Contains(callsLog(t, dir), "project delete") {
		t.Fatalf("nothing may be deleted on a listing failure:\n%s", callsLog(t, dir))
	}
	put(t, dir, "workspaces.json", `[]`)
	if got := prune(t, d); len(got) != 0 {
		t.Fatalf("the first successful sweep is the first idle sighting, removed %v", got)
	}
}

// A failed delete is retried on the next sweep rather than forgotten.
func TestPruneProjectsRetriesFailedDelete(t *testing.T) {
	d, dir, home := projectFixture(t)
	checkout := filepath.Join(home, ".conductor", "checkouts", "w")
	put(t, dir, "projects.json", `[{"projectId":"prj_c","name":"w","path":"`+checkout+`"}]`)
	put(t, dir, "project-delete-fail", "")

	prune(t, d)
	if got := prune(t, d); len(got) != 0 {
		t.Fatalf("a failed delete is not a removal, got %v", got)
	}
	if err := os.Remove(filepath.Join(dir, "project-delete-fail")); err != nil {
		t.Fatal(err)
	}
	if got := prune(t, d); len(got) != 1 {
		t.Fatalf("the still-idle project should be removed on retry, got %v", got)
	}
}

// A sweep must wait out an in-progress workspace creation: otherwise the
// workspace could land between its "anything active?" listing and the delete,
// and paseo would archive it out from under the agent about to run there.
func TestPruneProjectsWaitsForWorkspaceCreation(t *testing.T) {
	d, _, _ := projectFixture(t)
	release := holdProjects()
	done := make(chan struct{})
	go func() {
		_, _ = d.PruneProjects(context.Background())
		close(done)
	}()
	select {
	case <-done:
		release()
		t.Fatal("prune ran while a workspace creation held the guard")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("prune never ran after the creation released the guard")
	}
}

func TestIsConductorProjectPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	local := &Dispatcher{}
	remote := &Dispatcher{Remote: &hosts.Target{Name: "box"}}
	for _, tc := range []struct {
		d    *Dispatcher
		path string
		want bool
	}{
		{local, filepath.Join(home, ".conductor", "checkouts", "RosterStream"), true},
		{local, filepath.Join(home, ".conductor", "runs", "cron-7-abc"), true},
		{local, filepath.Join(home, ".conductor", "checkouts"), false},
		{local, filepath.Join(home, ".conductor"), false},
		{local, filepath.Join(home, ".conductor", "other", "x"), false},
		{local, filepath.Join(home, "Projects", "rosterstream"), false},
		{local, "/elsewhere/.conductor/checkouts/x", false},
		{local, "", false},
		{remote, "/home/bot/.conductor/checkouts/x", true},
		{remote, "/srv/work/.conductor/runs/cron-1-ff", true},
		{remote, ".conductor/checkouts/x", true},
		{remote, "/home/bot/.conductor/checkouts", false},
		{remote, "/home/bot/projects/x", false},
	} {
		if got := tc.d.isConductorProjectPath(tc.path); got != tc.want {
			t.Errorf("isConductorProjectPath(%q, remote=%v) = %v, want %v", tc.path, tc.d.remote(), got, tc.want)
		}
	}
}

// gitRepoUnder makes a real git checkout (origin set) at path.
func gitRepoUnder(t *testing.T, path, origin string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(gitRepoAt(t, "main", origin), path); err != nil {
		t.Fatal(err)
	}
	return path
}

// The case that made conductor keep its own clone: your project for the repo is
// named differently from the repo slug ("acme/widget" for AcmeCorp/Widget) and
// may have no workspace open. Matched by its origin remote, it is used — and
// preferred over conductor's clone of the same repo — with no clone attempted.
func TestResolveCheckoutDirFindsYourProjectByRemote(t *testing.T) {
	d, dir, home := projectFixture(t)
	d.repoDirs = map[string]string{}
	mine := gitRepoUnder(t, filepath.Join(home, "Projects", "widget"), "git@github.com:AcmeCorp/Widget.git")
	clone := gitRepoUnder(t, filepath.Join(home, ".conductor", "checkouts", "Widget"), "git@github.com:AcmeCorp/Widget.git")
	put(t, dir, "projects.json", `[
		{"projectId":"prj_clone","name":"Widget","path":"`+clone+`"},
		{"projectId":"prj_other","name":"acme/other","path":"`+gitRepoAt(t, "main", "git@github.com:AcmeCorp/Other.git")+`"},
		{"projectId":"prj_mine","name":"acme/widget","path":"`+mine+`"}
	]`)

	got, err := d.resolveCheckoutDir(context.Background(), "AcmeCorp/Widget")
	if err != nil {
		t.Fatal(err)
	}
	if got != mine {
		t.Fatalf("want your checkout %s, got %s", mine, got)
	}
	if strings.Contains(callsLog(t, dir), "clone") {
		t.Fatalf("a matching project must not trigger a clone:\n%s", callsLog(t, dir))
	}
}

// With only conductor's own clone registered, that is what gets reused.
func TestFindProjectDirFallsBackToConductorClone(t *testing.T) {
	d, dir, home := projectFixture(t)
	clone := gitRepoUnder(t, filepath.Join(home, ".conductor", "checkouts", "Widget"), "https://github.com/AcmeCorp/Widget")
	put(t, dir, "projects.json", `[{"projectId":"prj_clone","name":"Widget","path":"`+clone+`"}]`)
	if got := d.findProjectDir(context.Background(), "acmecorp/widget"); got != clone {
		t.Fatalf("want conductor's clone %s, got %q", clone, got)
	}
}

// A project matches by name (a remapped core.Target.Project) or by the remote
// paseo keyed it on; a checkout whose remote is a different repo never does,
// even one whose name merely contains the slug.
func TestProjectMatchesRepo(t *testing.T) {
	d, _, _ := projectFixture(t)
	near := gitRepoAt(t, "main", "git@github.com:AcmeCorp/Widget-old.git")
	for _, tc := range []struct {
		p    ProjectInfo
		repo string
		want bool
	}{
		{ProjectInfo{Name: "acme/widget", Path: "/nonexistent"}, "acme/widget", true},
		{ProjectInfo{ProjectID: "remote:github.com/AcmeCorp/Widget", Path: "/nonexistent"}, "acmecorp/widget", true},
		{ProjectInfo{ProjectID: "prj_1", Name: "x", Path: near}, "AcmeCorp/Widget", false},
		{ProjectInfo{ProjectID: "prj_2", Name: "x", Path: "/nonexistent"}, "AcmeCorp/Widget", false},
	} {
		if got := d.projectMatchesRepo(context.Background(), tc.p, tc.repo); got != tc.want {
			t.Errorf("projectMatchesRepo(%+v, %q) = %v, want %v", tc.p, tc.repo, got, tc.want)
		}
	}
}

func TestRepoSlug(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:AcmeCorp/Widget.git\n":        "AcmeCorp/Widget",
		"https://github.com/AcmeCorp/Widget":          "AcmeCorp/Widget",
		"https://github.com/AcmeCorp/Widget.git/":     "AcmeCorp/Widget",
		"ssh://git@github.com:22/AcmeCorp/Widget.git": "AcmeCorp/Widget",
		"github.com/AcmeCorp/Widget":                  "AcmeCorp/Widget",
		"Widget":                                      "",
		"":                                            "",
	} {
		if got := repoSlug(in); got != want {
			t.Errorf("repoSlug(%q) = %q, want %q", in, got, want)
		}
	}
}
