package bot

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// The memory surface a session reads through: its index in the prompt,
// ranked recall, reads in parts — for its own memory and its team's
// knowledge alike.

// MemoryIndex is the memory section of a session's prompt: the bot's
// memory index within its index_budget, and its team's knowledge index
// within the team's, both ranked against query (what the session is about).
// An index that cannot be built says so and points to recall: a prompt is
// never held up by it.
func (s *Service) MemoryIndex(ctx context.Context, name, query string) string {
	m := s.Cfg().Bots[name]
	text, err := s.Repo(name).RenderIndex(ctx, IndexView{Budget: m.Memory.IndexBudgetOr(config.DefaultIndexBudget), Query: query})
	if err != nil {
		s.logf("%s: memory index: %v", name, err)
		text = fmt.Sprintf("## Your memory index\n\n(unavailable just now: %v) — find documents with `conductor bot recall <words>`.\n", err)
	}
	if team, _, ok := s.Cfg().TeamOf(name); ok {
		if repo := s.TeamRepo(team); repo != nil && repo.Init(ctx) == nil {
			t, err := repo.RenderIndex(ctx, IndexView{Budget: s.Cfg().TeamLimits(team).IndexBudgetOr(config.DefaultTeamIndexBudget), Query: query,
				Heading: "## Team " + team + "'s knowledge index", Recall: "conductor bot team recall", IndexCmd: "conductor bot team index"})
			if err != nil {
				s.logf("team %s: knowledge index: %v", team, err)
			} else {
				text = strings.TrimRight(text, "\n") + "\n\n" + t
			}
		}
	}
	return text
}

// homeQuery is what a home session is about at launch: its open cases.
func homeQuery(rec store.BotRecord) string {
	var parts []string
	var open []*store.Case
	for _, c := range rec.Cases {
		if store.Active(c.Status) {
			open = append(open, c)
		}
	}
	sort.Slice(open, func(i, j int) bool { return open[i].Created.After(open[j].Created) })
	for i, c := range open {
		if i == 10 {
			break
		}
		parts = append(parts, c.Title, c.Summary)
	}
	return strings.Join(parts, " ")
}

// recallOut renders hits for a session: read and index are the commands
// that read a hit and list the repo.
func recallOut(hits []Hit, read, index string) map[string]any {
	out := make([]any, 0, len(hits))
	for _, h := range hits {
		e := map[string]any{"path": h.Path, "kind": h.Kind, "title": h.Title, "snippet": h.Snippet}
		if h.Archived != "" {
			e["archived"] = h.Archived
		}
		out = append(out, e)
	}
	res := map[string]any{"hits": out, "count": len(out)}
	switch {
	case len(out) > 0 && strings.HasSuffix(read, " read"):
		res["note"] = "Best match first. Read what you need: `" + read + " <path> --section \"<heading>\"` (sections are listed when a read is cut)."
	case len(out) > 0:
		res["note"] = "Best match first. Read one: `" + read + " <path>`."
	default:
		res["note"] = "No document matched. Try other words, or list what there is: `" + index + "`."
	}
	return res
}

// recallLimit is a session's --limit, bounded.
func recallLimit(args map[string]any, l config.MemoryLimits) (int, error) {
	n := l.RecallLimitOr()
	if v := str(args, "limit"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 1 || n > config.MaxRecallLimit {
			return 0, fmt.Errorf("--limit: want 1 to %d, got %q", config.MaxRecallLimit, v)
		}
	}
	return n, nil
}

func intArg(args map[string]any, k string) (int, error) {
	v := str(args, k)
	if v == "" {
		return 0, nil
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return 0, fmt.Errorf("--%s: want a number, got %q", k, v)
	}
	return n, nil
}

// memRecall, memRead and memIndex serve a session's recall, read and index
// against a repo (its memory, or its team's knowledge).
func memRecall(ctx context.Context, repo *Repo, args map[string]any, read, index string) (map[string]any, error) {
	n, err := recallLimit(args, repo.Limits())
	if err != nil {
		return nil, err
	}
	if k := str(args, "kind"); k != "" {
		if _, err := repo.Kinds().Get(k); err != nil {
			return nil, err
		}
	}
	hits, err := repo.Recall(ctx, str(args, "query"), n, str(args, "kind"))
	if err != nil {
		return nil, err
	}
	return recallOut(hits, read, index), nil
}

func memRead(ctx context.Context, repo *Repo, args map[string]any) (map[string]any, error) {
	off, err := intArg(args, "offset")
	if err != nil {
		return nil, err
	}
	res, err := repo.ReadPart(ctx, str(args, "path"), str(args, "section"), off, 0)
	if err != nil {
		return nil, err
	}
	return toMap(res), nil
}

func memIndex(ctx context.Context, repo *Repo, args map[string]any, v IndexView) (map[string]any, error) {
	kind := str(args, "kind")
	if kind != "" {
		if _, err := repo.Kinds().Get(kind); err != nil {
			return nil, err
		}
	}
	if kind != "" || boolArg(args, "all") || str(args, "offset") != "" {
		off, err := intArg(args, "offset")
		if err != nil {
			return nil, err
		}
		return repo.IndexPage(ctx, kind, off, 0)
	}
	text, err := repo.RenderIndex(ctx, v)
	if err != nil {
		return nil, err
	}
	return map[string]any{"text": text}, nil
}
