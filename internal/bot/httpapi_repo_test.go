package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func repoJSON(t *testing.T, h http.Handler, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd *strings.Reader
	ct := ""
	if body != nil {
		b, _ := json.Marshal(body)
		rd, ct = strings.NewReader(string(b)), "application/json"
	}
	var w interface {
		Result() *http.Response
	}
	if rd != nil {
		w = apiDo(t, h, method, path, rd, ct)
	} else {
		w = apiDo(t, h, method, path, nil, "")
	}
	res := w.Result()
	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// THE REPO API: a bot's memory and a team's knowledge read as the browser
// needs them, an operator's write lands as one commit against the head it
// read, a stale head is refused with the current one, and unknown targets
// are 404s.
func TestOperatorAPIRepo(t *testing.T) {
	f := newTeamFixture(t, nil)
	h := OperatorAPI(f.svc)

	code, mem := repoJSON(t, h, "GET", "/v1/bots/a/memory", nil)
	if code != 200 || mem["target"] != "bot:a" || mem["team"] != "platform" || mem["team_knowledge"] != true {
		t.Fatalf("a's memory: %d %v", code, mem)
	}
	kinds := map[string]map[string]any{}
	for _, k := range mem["kinds"].([]any) {
		km := k.(map[string]any)
		kinds[km["kind"].(string)] = km
	}
	if kinds["knowledge"]["operator"] != true || kinds["log"]["operator"] != false || kinds["log"]["annotate"] != true {
		t.Fatalf("kinds: %v", kinds)
	}
	head := mem["head"].(string)

	// A new knowledge entry: its path follows from the title.
	code, out := repoJSON(t, h, "POST", "/v1/bots/a/memory/new?operator=op", map[string]any{"kind": "knowledge", "title": "DB primary",
		"text": "---\nevidence: seen\nconfidence: high\n---\ndb-prod-1 is the primary\n", "base": head})
	if code != 200 || out["path"] != "knowledge/db-primary.md" || out["changed"] != true {
		t.Fatalf("new: %d %v", code, out)
	}
	h1 := out["commit"].(string)
	code, mem = repoJSON(t, h, "GET", "/v1/bots/a/memory", nil)
	found := false
	for _, x := range mem["files"].([]any) {
		fm := x.(map[string]any)
		if fm["path"] == "knowledge/db-primary.md" {
			found = fm["title"] == "DB primary" && fm["kind"] == "knowledge" && strings.Contains(fm["text"].(string), "db-prod-1")
		}
	}
	if code != 200 || !found {
		t.Fatalf("the new entry isn't listed: %v", mem["files"])
	}
	if !strings.HasPrefix(h1, mem["head"].(string)[:7]) && mem["head"] != h1 {
		t.Fatalf("head %v after commit %v", mem["head"], h1)
	}

	// A write against the old head is refused with the current one.
	code, out = repoJSON(t, h, "POST", "/v1/bots/a/memory/edit?operator=op", map[string]any{"op": "put", "path": "knowledge/db-primary.md",
		"text": kd("stale"), "base": head})
	if code != http.StatusConflict || out["head"] == nil || !strings.Contains(out["error"].(string), "stale base") {
		t.Fatalf("stale base: %d %v", code, out)
	}
	// Against the head it lands; history and the commit's diff show it.
	code, out = repoJSON(t, h, "POST", "/v1/bots/a/memory/edit?operator=op", map[string]any{"op": "put", "path": "knowledge/db-primary.md",
		"text": kd("db-prod-2 is the primary now"), "base": h1, "message": "failover"})
	if code != 200 || out["changed"] != true {
		t.Fatalf("put: %d %v", code, out)
	}
	h2 := out["commit"].(string)
	code, hist := repoJSON(t, h, "GET", "/v1/bots/a/memory/history?path=knowledge/db-primary.md", nil)
	cs, _ := hist["commits"].([]any)
	if code != 200 || len(cs) != 2 || cs[0].(map[string]any)["subject"] != "failover" || cs[0].(map[string]any)["kind"] != "operator" {
		t.Fatalf("history: %d %v", code, hist)
	}
	code, cm := repoJSON(t, h, "GET", "/v1/bots/a/memory/commits/"+h2, nil)
	fs, _ := cm["files"].([]any)
	if code != 200 || len(fs) != 1 || !strings.Contains(fs[0].(map[string]any)["before"].(string), "db-prod-1") ||
		!strings.Contains(fs[0].(map[string]any)["after"].(string), "db-prod-2") {
		t.Fatalf("commit view: %d %v", code, cm)
	}
	code, bl := repoJSON(t, h, "GET", "/v1/bots/a/memory/blame?path=knowledge/db-primary.md", nil)
	gs, _ := bl["groups"].([]any)
	if code != 200 || len(gs) == 0 || gs[0].(map[string]any)["who"] != "op" {
		t.Fatalf("blame: %d %v", code, bl)
	}
	// A document that fails its kind's schema is refused, with why.
	code, out = repoJSON(t, h, "POST", "/v1/bots/a/memory/new?operator=op", map[string]any{"kind": "knowledge", "title": "No evidence", "base": h2,
		"text": "---\nconfidence: high\n---\nbody\n"})
	if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "evidence") {
		t.Fatalf("schema: %d %v", code, out)
	}

	// Unknown bot or team, a team with no knowledge repo: 404.
	for _, p := range []string{"/v1/bots/nobody/memory", "/v1/teams/nowhere/knowledge", "/v1/teams/billing/knowledge"} {
		if code, out := repoJSON(t, h, "GET", p, nil); code != http.StatusNotFound {
			t.Fatalf("%s: %d %v", p, code, out)
		}
	}
	if code, out := repoJSON(t, h, "POST", "/v1/bots/nobody/memory/edit?operator=op", map[string]any{"op": "rm", "path": "knowledge/x.md", "base": h2}); code != http.StatusNotFound {
		t.Fatalf("write to an unknown bot: %d %v", code, out)
	}

	// Only an operator of the bot writes its memory (a is op's alone).
	code, out = repoJSON(t, h, "POST", "/v1/bots/a/memory/edit?operator=op2", map[string]any{"op": "rm", "path": "knowledge/db-primary.md", "base": h2})
	if code != http.StatusForbidden {
		t.Fatalf("op2 writing a's memory: %d %v", code, out)
	}
}

// A team's knowledge: written by its operators, and what its bots proposed
// listed for the Suggested-by-bots panel with the text and the current file.
func TestOperatorAPITeamKnowledge(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	h := OperatorAPI(f.svc)
	code, kn := repoJSON(t, h, "GET", "/v1/teams/platform/knowledge", nil)
	if code != 200 || kn["target"] != "team:platform" || kn["promotion"] != "proposal" || !strings.HasSuffix(kn["repo"].(string), "/team-platform") {
		t.Fatalf("team knowledge: %d %v", code, kn)
	}
	code, out := repoJSON(t, h, "POST", "/v1/teams/platform/knowledge/edit?operator=op2", map[string]any{"op": "put", "kind": "knowledge",
		"path": "knowledge/databases.md", "text": kd("db-main is the primary"), "create": true, "base": kn["head"]})
	if code != 200 || out["operator"] != "op2" {
		t.Fatalf("team write: %d %v", code, out)
	}
	if code, out := repoJSON(t, h, "POST", "/v1/teams/platform/knowledge/edit?operator=bop", map[string]any{"op": "rm",
		"path": "knowledge/databases.md", "base": out["commit"]}); code != http.StatusForbidden {
		t.Fatalf("bop (billing's) writing platform's knowledge: %d %v", code, out)
	}
	if _, _, err := f.svc.WriteMemory(ctx, f.homeOf("a"), MemoryWrite{Path: "knowledge/db.md", Text: kd("db-prod-1 is the primary")}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Promote(ctx, f.homeOf("a"), "knowledge/db.md", "knowledge/databases.md", "the team keeps asking"); err != nil {
		t.Fatal(err)
	}
	code, ps := repoJSON(t, h, "GET", "/v1/teams/platform/knowledge/proposals", nil)
	list, _ := ps["proposals"].([]any)
	if code != 200 || len(list) != 1 {
		t.Fatalf("proposals: %d %v", code, ps)
	}
	p := list[0].(map[string]any)
	if p["bot"] != "a" || p["status"] != "pending" || p["to"] != "knowledge/databases.md" || p["kind"] != "knowledge" ||
		!strings.Contains(p["text"].(string), "db-prod-1") || !strings.Contains(p["current"].(string), "db-main") || p["note"] != "the team keeps asking" {
		t.Fatalf("proposal row: %v", p)
	}
}
