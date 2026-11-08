package dashboard

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
)

// Packs → Browse (CONFORMANCE "packs-browse"): the same catalog
// `conductor pack search` serves — installed packs, then the registry's
// (official by default, or Deps.PackRegistry) — read through
// config.PackCatalog, core's one generic read model for "what packs exist",
// shared with the CLI. Each entry also carries its install review
// (config.BuildPackReview, the function `conductor pack add` prints), read
// from the manifest the catalog already loaded, so a pack's Details page and
// its Add review need no second fetch. Nothing pack-specific lives here
// beyond rendering those.

// packFetchTimeout bounds a registry refresh or a source fetch (a git clone
// or a local copy) to a request the operator is actively waiting on; a slow
// or hung remote reports in plain words rather than hanging the handler.
var packFetchTimeout = 30 * time.Second

// withFetchTimeout runs fn off the handler goroutine and gives up after
// packFetchTimeout (or when the request goes away). The fetch itself is also
// bounded by git's own command timeout and cleans its scratch dir up when it
// returns, so an abandoned one finishes on its own.
func withFetchTimeout[T any](ctx context.Context, fn func() T) (T, bool) {
	ctx, cancel := context.WithTimeout(ctx, packFetchTimeout)
	defer cancel()
	done := make(chan T, 1)
	go func() { done <- fn() }()
	select {
	case v := <-done:
		return v, true
	case <-ctx.Done():
		var zero T
		return zero, false
	}
}

func (s *Server) handlePacksCatalog(w http.ResponseWriter, r *http.Request) {
	if s.deps.ConfigPath == "" {
		writeError(w, http.StatusConflict, "read_only", "no config path is configured", "")
		return
	}
	q := r.URL.Query().Get("q")
	result, ok := withFetchTimeout(r.Context(), func() config.CatalogResult {
		return config.PackCatalog(s.deps.ConfigPath, q, s.deps.PackRegistry, false, false)
	})
	if !ok {
		// The registry didn't answer in time: still list what's installed.
		result = config.PackCatalog(s.deps.ConfigPath, q, s.deps.PackRegistry, true, false)
		result.RegistryError = "the pack registry took too long to answer"
	}
	tree := s.liveTree()
	connByKind := connectorKindMap(tree)
	resp := api.CatalogResponse{
		HiddenTemplatePacks: result.HiddenTemplatePacks,
		RegistryProblems:    result.RegistryProblems,
		RegistryError:       result.RegistryError,
		ConfigError:         result.ConfigError,
		Packs:               []api.CatalogPackInfo{},
		Instances:           sortedKeys(mapOf(tree["packs"])),
	}
	for _, p := range result.Packs {
		ci := api.CatalogPackInfo{
			Name: p.Name, Version: p.Version, Description: p.Description,
			Contents: p.Contents.Counts, TemplateOnly: p.TemplateOnly,
			Installed: p.Installed, Instance: p.Instance,
			Source: p.Source, Install: p.Install, Requires: p.Requires, Latest: p.Latest,
		}
		if ref, err := config.ParsePackRef(p.Source); err == nil {
			ci.Official = ref.Official
			ci.InstallKey, ci.InstallRef, ci.Local = ref.Key, ref.Value, ref.Local
		}
		if p.Manifest != nil {
			rv := reviewOf(config.BuildPackReview(p.Manifest), connByKind)
			ci.Review = &rv
		}
		resp.Packs = append(resp.Packs, ci)
	}
	writeJSON(w, http.StatusOK, resp)
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// reviewOf renders config.PackReview for the dashboard, with each connector
// role's candidates among this config's connections (what Add offers to bind
// it to; exactly one is bound automatically).
func reviewOf(rv config.PackReview, connByKind map[string][]string) api.PackReview {
	out := api.PackReview{
		Name: rv.Name, Version: rv.Version, Description: rv.Description,
		Stores: rv.Stores, Secrets: rv.Secrets,
		Workflows: rv.Workflows, Bots: rv.Bots, AgentSteps: rv.AgentSteps,
	}
	for _, c := range rv.Connectors {
		out.Connectors = append(out.Connectors, api.PackRoleInfo{Name: c, Kind: "connector", Candidates: connByKind[c]})
	}
	for _, g := range rv.Grants {
		out.Grants = append(out.Grants, api.PackReviewGrant{Where: g.Where, Verbs: g.Verbs, Secrets: g.Secrets})
	}
	for _, t := range rv.Triggers {
		out.Triggers = append(out.Triggers, api.PackReviewTrigger{Name: t.Name, On: t.On, Connector: strings.SplitN(t.On, ".", 2)[0]})
	}
	return out
}

// handlePackReview fetches a pack source (never installs or writes config)
// and returns its install review, reusing config.BuildPackReview — the same
// function `conductor pack add` renders as text. The operator confirms by
// staging packs.<instance> through the normal POST /api/v1/config/stage, so
// Add never bypasses staging, review or step-up.
func (s *Server) handlePackReview(w http.ResponseWriter, r *http.Request) {
	if s.deps.ConfigPath == "" {
		writeError(w, http.StatusConflict, "read_only", "no config path is configured", "")
		return
	}
	var req api.PackReviewRequest
	if !decodeBody(w, r, &req) {
		return
	}
	ref, err := config.ParsePackRef(req.Source)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "that isn't a pack source conductor can read: "+err.Error(), "an owner/repo path, a git URL, or a local folder such as ./packs/house-style")
		return
	}
	baseDir := filepath.Dir(s.deps.ConfigPath)
	type result struct {
		man *config.PackManifest
		err error
	}
	res, ok := withFetchTimeout(r.Context(), func() result {
		man, err := config.FetchPackForReview(ref.Fetch, baseDir)
		return result{man, err}
	})
	if !ok {
		writeError(w, http.StatusGatewayTimeout, "timeout", "fetching that source took too long", "check the source and try again")
		return
	}
	if res.err != nil {
		writeError(w, http.StatusBadRequest, "fetch", "couldn't read a pack there: "+res.err.Error(), "")
		return
	}
	tree := s.liveTree()
	rv := config.BuildPackReview(res.man)
	resp := api.PackReviewResponse{
		Review:      reviewOf(rv, connectorKindMap(tree)),
		Instance:    suggestInstance(rv.Name, mapOf(tree["packs"])),
		InstallKey:  ref.Key,
		InstallRef:  ref.Value,
		Local:       ref.Local,
		Official:    ref.Official,
		ConfigValue: config.PackConfigBlock(ref),
	}
	writeJSON(w, http.StatusOK, resp)
}

// suggestInstance is the pack's own name, or name-2, -3… when an instance
// by that name already exists (the same pack can be installed twice).
func suggestInstance(name string, packs map[string]any) string {
	if _, taken := packs[name]; !taken {
		return name
	}
	for i := 2; ; i++ {
		c := name + "-" + strconv.Itoa(i)
		if _, taken := packs[c]; !taken {
			return c
		}
	}
}
