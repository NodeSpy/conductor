package bot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// section is the text from a heading to the next "## " heading.
func promptSection(prompt, heading string) string {
	i := strings.Index(prompt, heading)
	if i < 0 {
		return ""
	}
	rest := prompt[i+len(heading):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return heading + rest
}

// A SESSION STARTS FROM ITS INDEX: the first prompt carries the memory
// index and the read rules, never an instruction to read the repo.
func TestHomePromptCarriesTheIndex(t *testing.T) {
	f := newSvc(t)
	ctx := context.Background()
	writeDoc(t, f, "knowledge", "db", know("DB primary", "db-main", "The primary is db-1.", "body"))
	f.svc.Tick(ctx, "sre")
	p := f.home.launched[0].Prompt
	for _, want := range []string{"## Your memory index (1 documents)", "- knowledge/db.md · knowledge · DB primary · for: db-main · The primary is db-1.",
		"RECALL before you open a file", "READ SECTIONS", "conductor bot read <path> [--section", "ONE TOPIC PER DOCUMENT", "first session"} {
		if !strings.Contains(p, want) {
			t.Errorf("home prompt lacks %q", want)
		}
	}
	for _, bad := range []string{"Read README.md", "and knowledge/ to orient", "Read it freely"} {
		if strings.Contains(p, bad) {
			t.Errorf("home prompt still says %q", bad)
		}
	}
	if strings.Index(p, "## Your memory index") > strings.Index(p, "## Where you left off") {
		t.Error("the index comes after the handoff")
	}
}

// A worker's index is ranked against its brief: the document it needs is
// listed first, however old; and its team's knowledge index follows.
func TestWorkerIndexRankedByBrief(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	repo := f.svc.Repo("b")
	for i := 0; i < 120; i++ {
		p := fmt.Sprintf("knowledge/fact-%03d.md", i)
		_ = os.MkdirAll(filepath.Join(repo.Dir, "knowledge"), 0o700)
		_ = os.WriteFile(filepath.Join(repo.Dir, p), []byte(know(fmt.Sprintf("Fact %d about the web tier", i), "", "", "nothing special")), 0o600)
	}
	_ = os.WriteFile(filepath.Join(repo.Dir, "knowledge", "kafka.md"), []byte(know("Kafka consumer lag", "kafka", "Lag above 10k means the consumer is stuck.", "")), 0o600)
	_, _ = repo.git(ctx, repo.Dir, "add", "-A")
	_, _ = repo.git(ctx, repo.Dir, "commit", "-q", "--date=2020-01-01T00:00:00Z", "-m", "old facts")
	// Newer documents than the one the brief needs.
	for i := 0; i < 3; i++ {
		if _, _, err := f.svc.WriteMemory(ctx, f.homeOf("b"), MemoryWrite{Kind: "knowledge", Slug: fmt.Sprintf("new-%d", i), Text: know(fmt.Sprintf("New %d", i), "", "", "x")}); err != nil {
			t.Fatal(err)
		}
	}
	idx := f.svc.MemoryIndex(ctx, "b", "the kafka consumer is lagging, find out why")
	mine := promptSection(idx, "## Your memory index")
	first := strings.SplitN(strings.SplitN(mine, "\n\n", 2)[1], "\n", 2)[0]
	if !strings.HasPrefix(first, "- knowledge/kafka.md") {
		t.Fatalf("the relevant document is not first: %q", first)
	}
	if len(mine) > config.DefaultIndexBudget || !strings.Contains(mine, "more not shown") {
		t.Fatalf("bot index: %d bytes", len(mine))
	}
	team := promptSection(idx, "## Team platform's knowledge index")
	if team == "" || !strings.Contains(team, "conductor bot team recall") && !strings.Contains(team, "(0 documents)") {
		t.Fatalf("team index missing:\n%s", idx)
	}
	if len(team) > config.DefaultTeamIndexBudget {
		t.Fatalf("team index over its budget: %d", len(team))
	}
	// A bot without a team has none.
	if strings.Contains(f.svc.MemoryIndex(ctx, "solo", ""), "knowledge index") {
		t.Fatal("a solo bot got a team index")
	}
}

// synthMemory writes a large memory repo in one commit: n knowledge
// documents, r runbooks, e raw episodes — realistic sizes.
func synthMemory(t testing.TB, dir string, n, r, e int) (knowledgeBytes int) {
	t.Helper()
	para := "The service runs three replicas behind the internal load balancer; health checks hit /healthz every ten seconds and a replica is drained after three failures. Deploys roll one replica at a time. "
	write := func(p, s string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o700)
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		doc := fmt.Sprintf("---\ntitle: Service %d topology\napplies_to: [svc-%d, prod]\nevidence: |\n  Observed during incident %d and confirmed in the deploy config.\nconfidence: medium\nsummary: How svc-%d is deployed, scaled and health-checked.\n---\n## Topology\n\n%s\n\n## Scaling\n\n%s\n\n## Gotchas\n\n%s\n",
			i, i, i, i, strings.Repeat(para, 3), strings.Repeat(para, 2), para)
		knowledgeBytes += len(doc)
		write(fmt.Sprintf("knowledge/service-%03d.md", i), doc)
	}
	for i := 0; i < r; i++ {
		write(fmt.Sprintf("runbooks/procedure-%03d.md", i), fmt.Sprintf("---\ntitle: Procedure %d\nwhen_to_use: svc-%d is degraded\nsteps:\n  - check the dashboards\n  - drain the bad replica\n  - roll back the last deploy\nverification: error rate back under 1%%\nrollback: redeploy the previous version\nowner: platform\n---\n## Notes\n\n%s\n", i, i, strings.Repeat(para, 4)))
	}
	day := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < e; i++ {
		d := day.Add(time.Duration(i) * 6 * time.Hour)
		write(fmt.Sprintf("log/%s/%s-c%d-incident-%d.md", d.Format("2006/01"), d.Format("2006-01-02"), i, i),
			fmt.Sprintf("---\ntitle: incident %d\ncase: c%d\n---\n## %s — home\n\nPaged for svc-%d. %s\n\n## %s — worker w%d done\n\nResult: mitigated by draining a replica.\n", i, i, d.Format(time.RFC3339), i%n, strings.Repeat(para, 3), d.Format(time.RFC3339), i))
	}
	return knowledgeBytes
}

// THE MEASUREMENT: a large memory — 500 knowledge documents, 100
// runbooks, 2,000 episodes — and the session-start prompt stays within
// budget: the index within index_budget, the whole prompt within the
// fixed sections plus the index budget and the handoff cap.
func TestSessionStartPromptStaysUnderBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 2,600-document memory repo")
	}
	f := newSvc(t)
	ctx := context.Background()
	repo := f.svc.Repo("sre")
	// What the old first prompt made a session read: README.md and all of
	// knowledge/.
	readme, _ := repo.Read("README.md")
	kb := synthMemory(t, repo.Dir, 500, 100, 2000)
	_, _ = repo.git(ctx, repo.Dir, "add", "-A")
	if _, err := repo.git(ctx, repo.Dir, "commit", "-q", "-m", "a large memory"); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	if err := repo.SyncIndex(ctx); err != nil {
		t.Fatal(err)
	}
	rebuild := time.Since(t0)
	// A full handoff note at its cap, so the prompt carries the most it can.
	hnote := strings.Repeat("in flight: something to continue. ", config.DefaultMaxBytes["handoff"]/34)
	if _, err := repo.AppendLog(ctx, repo.HandoffPath("s0"), map[string]any{"session": "s0"}, "# Handoff from session s0\n\n"+hnote, "handoff", nil); err != nil {
		t.Fatal(err)
	}
	t0 = time.Now()
	f.svc.Tick(ctx, "sre")
	launch := time.Since(t0)
	if len(f.home.launched) != 1 {
		t.Fatal("no home session")
	}
	prompt := f.home.launched[0].Prompt
	index := promptSection(prompt, "## Your memory index")
	budget := config.DefaultIndexBudget
	if len(index) > budget {
		t.Fatalf("index section %d bytes, over its %d budget", len(index), budget)
	}
	if !strings.Contains(index, "(600 documents)") || !strings.Contains(index, "2000 raw case logs") || !strings.Contains(index, "more not shown") {
		t.Fatalf("index:\n%s", index)
	}
	// The fixed part: the same prompt for an empty memory and no handoff.
	fixed := len(HomePrompt(f.cfg, "sre", f.st.Bot("sre"), "", "", ""))
	limit := fixed + budget + config.DefaultMaxBytes["handoff"] + 1024
	if len(prompt) > limit {
		t.Fatalf("session-start prompt %d bytes, over %d (fixed %d + index %d + handoff %d + 1KiB)", len(prompt), limit, fixed, budget, config.DefaultMaxBytes["handoff"])
	}
	before := len(HomePrompt(f.cfg, "sre", store.BotRecord{}, "", "", "")) + len(readme) + kb
	firstAfter := len(HomePrompt(f.cfg, "sre", f.st.Bot("sre"), "", "", index))
	t.Logf("first session (no handoff) with the index: %d chars (~%d tokens)", firstAfter, firstAfter/4)
	t.Logf("MEASUREMENT (500 knowledge, 100 runbooks, 2000 episodes): session start, before = prompt + README.md + knowledge/ it was told to read = %d chars (~%d tokens); after = prompt with the index and a full handoff = %d chars (~%d tokens), of which the index %d chars; index rebuild from nothing %v, launch (index sync + render) %v",
		before, before/4, len(prompt), len(prompt)/4, len(index), rebuild.Round(time.Millisecond), launch.Round(time.Millisecond))
	// A worker's prompt is bounded the same way.
	w, err := f.svc.Spawn(ctx, homeCaller(f), SpawnRequest{Title: "svc-42 is degraded", Brief: "find out why svc-42 is degraded"})
	if err != nil {
		t.Fatal(err)
	}
	var wp string
	for _, x := range f.workers.fired {
		if x.inputs["worker"] == w.ID {
			wp, _ = x.inputs["prompt"].(string)
		}
	}
	wi := promptSection(wp, "## Your memory index")
	if len(wi) > budget || !strings.Contains(wi, "service-042.md") {
		t.Fatalf("worker index (%d bytes) lacks the relevant document:\n%s", len(wi), wi)
	}
	t.Logf("worker prompt: %d chars (~%d tokens)", len(wp), len(wp)/4)
	// Recall over all of it stays a ranked handful.
	hits, err := repo.Recall(ctx, "procedure 7 degraded", 8, "")
	if err != nil || len(hits) == 0 || hits[0].Path != "runbooks/procedure-007.md" {
		t.Fatalf("recall at scale: %v %v", paths(hits), err)
	}
}
