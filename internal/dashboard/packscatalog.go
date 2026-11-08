package dashboard

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
)

// Packs → Browse (CONFORMANCE "packs-browse"): the same catalog
// `conductor pack search` serves — installed packs, then the registry's
// (official by default, or Deps.PackRegistry) — read through
// config.PackCatalog, core's one generic read model for "what packs exist",
// shared with the CLI. Nothing pack-specific lives in the dashboard beyond
// rendering this.

func (s *Server) handlePacksCatalog(w http.ResponseWriter, r *http.Request) {
	if s.deps.ConfigPath == "" {
		writeError(w, http.StatusConflict, "read_only", "no config path is configured", "")
		return
	}
	q := r.URL.Query().Get("q")
	result := config.PackCatalog(s.deps.ConfigPath, q, s.deps.PackRegistry, false, false)
	resp := api.CatalogResponse{
		HiddenTemplatePacks: result.HiddenTemplatePacks,
		RegistryProblems:    result.RegistryProblems,
		RegistryError:       result.RegistryError,
		ConfigError:         result.ConfigError,
	}
	for _, p := range result.Packs {
		resp.Packs = append(resp.Packs, api.CatalogPackInfo{
			Name: p.Name, Version: p.Version, Description: p.Description,
			Contents: p.Contents.Counts, TemplateOnly: p.TemplateOnly,
			Installed: p.Installed, Instance: p.Instance,
			Official: strings.HasPrefix(p.Source, config.PacksNamespaceAlias+"/"),
			Source:   p.Source, Install: p.Install,
		})
	}
	if resp.Packs == nil {
		resp.Packs = []api.CatalogPackInfo{}
	}
	writeJSON(w, http.StatusOK, resp)
}

// packReviewTimeout bounds a source fetch (git clone or local copy) to a
// request the operator is actively waiting on; a slow or hung remote reports
// as unavailable rather than hanging the daemon's HTTP handler.
const packReviewTimeout = 30 * time.Second

// handlePackReview fetches a pack source (never installs or writes config)
// and returns its install review, reusing config.BuildPackReview — the same
// function `conductor pack add` renders as text. The operator confirms by
// staging the returned ConfigValue through the normal
// POST /api/v1/config/stage, so Add never bypasses staging or step-up.
func (s *Server) handlePackReview(w http.ResponseWriter, r *http.Request) {
	if s.deps.ConfigPath == "" {
		writeError(w, http.StatusConflict, "read_only", "no config path is configured", "")
		return
	}
	var req api.PackReviewRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Source == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "a pack source is required", "")
		return
	}
	baseDir := filepath.Dir(s.deps.ConfigPath)

	ctx, cancel := context.WithTimeout(r.Context(), packReviewTimeout)
	defer cancel()
	type result struct {
		man *config.PackManifest
		err error
	}
	done := make(chan result, 1)
	go func() {
		man, err := config.FetchPackForReview(req.Source, baseDir)
		done <- result{man, err}
	}()
	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "timeout", "fetching that source took too long", "check the source and try again")
		return
	}
	if res.err != nil {
		writeError(w, http.StatusBadRequest, "fetch", "couldn't fetch that pack: "+res.err.Error(), "")
		return
	}
	rv := config.BuildPackReview(res.man)
	resp := api.PackReviewResponse{
		Name: rv.Name, Version: rv.Version, Description: rv.Description,
		Connectors: rv.Connectors, Stores: rv.Stores, Secrets: rv.Secrets,
		Instance: rv.Name, ConfigValue: config.PackConfigBlock(req.Source, res.man),
	}
	for _, g := range rv.Grants {
		resp.Grants = append(resp.Grants, api.PackReviewGrant{Where: g.Where, Verbs: g.Verbs, Secrets: g.Secrets})
	}
	for _, t := range rv.Triggers {
		resp.Triggers = append(resp.Triggers, api.PackReviewTrigger{Name: t.Name, On: t.On})
	}
	writeJSON(w, http.StatusOK, resp)
}
