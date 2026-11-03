package pkgproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// NuGet serves a NuGet v3 feed (dotnet, nuget). The service index is
// conductor's own, offering only the flat container (PackageBaseAddress),
// which is all a restore uses. A .nupkg is verified against the package
// hash nuget.org publishes in its catalog (reached through the package's
// registration leaf) before it is cached; a feed without registrations
// (most private feeds) is cached by URL.
type NuGet struct{ *registry }

// DefaultNuGetUpstream is nuget.org's service index.
const DefaultNuGetUpstream = "https://api.nuget.org/v3/index.json"

// NewNuGet builds the NuGet ecosystem. Its upstream is a service index URL.
func NewNuGet(cfg RegistryConfig) (*NuGet, error) {
	r, err := newRegistry("nuget", DefaultNuGetUpstream, cfg)
	if err != nil {
		return nil, err
	}
	return &NuGet{r}, nil
}

// Name is the URL segment.
func (n *NuGet) Name() string { return "nuget" }

// AgentEnv quiets the dotnet CLI's own calls home.
func (n *NuGet) AgentEnv(string) []string {
	return []string{"DOTNET_CLI_TELEMETRY_OPTOUT=1", "DOTNET_NOLOGO=1", "DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1",
		"DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE=1", "DOTNET_GENERATE_ASPNET_CERTIFICATE=false"}
}

// HomeFiles makes the proxy the only package source.
func (n *NuGet) HomeFiles(base string) map[string]string {
	return map[string]string{".nuget/NuGet/NuGet.Config": `<?xml version="1.0" encoding="utf-8"?>
<!-- Written by conductor: packages come through its package proxy only. -->
<configuration>
  <packageSources>
    <clear />
    <add key="conductor" value="` + base + `/v3/index.json" allowInsecureConnections="true" />
  </packageSources>
</configuration>
`}
}

var (
	nugetID  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	nugetVer = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]{0,63}$`)
)

const nugetMetaMax = 32 << 20

// resources reads the upstream service index: the flat container and the
// registration base ("" when the feed has none).
func (n *NuGet) resources(ctx context.Context) (flat, reg string, err error) {
	b, err := n.meta.get(ctx, n.fetch, n.upstream, "", nugetMetaMax)
	if err != nil {
		return "", "", fmt.Errorf("service index: %w", err)
	}
	var idx struct {
		Resources []struct {
			ID   string `json:"@id"`
			Type string `json:"@type"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		return "", "", fmt.Errorf("service index: %w", err)
	}
	for _, r := range idx.Resources {
		switch r.Type {
		case "PackageBaseAddress/3.0.0":
			flat = strings.TrimRight(r.ID, "/")
		case "RegistrationsBaseUrl/3.6.0":
			reg = strings.TrimRight(r.ID, "/")
		case "RegistrationsBaseUrl", "RegistrationsBaseUrl/3.0.0-rc":
			if reg == "" {
				reg = strings.TrimRight(r.ID, "/")
			}
		}
	}
	if flat == "" {
		return "", "", fmt.Errorf("service index has no PackageBaseAddress")
	}
	return flat, reg, nil
}

// Serve routes v3/index.json and v3-flatcontainer/….
func (n *NuGet) Serve(w http.ResponseWriter, r *http.Request, c *Call) {
	bad := func(msg string) {
		st, m := n.fail(w, http.StatusBadRequest, msg)
		c.Emit(Event{Artifact: "request", Status: st, Err: m})
	}
	if c.Rest == "v3/index.json" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"version": "3.0.0", "resources": []map[string]string{
			{"@id": c.Base + "/v3-flatcontainer/", "@type": "PackageBaseAddress/3.0.0"},
		}})
		return
	}
	p, ok := strings.CutPrefix(c.Rest, "v3-flatcontainer/")
	if !ok {
		bad("not a service-index or flat-container path")
		return
	}
	f := strings.Split(p, "/")
	if !nugetID.MatchString(f[0]) || strings.Contains(f[0], "..") {
		bad("bad package id")
		return
	}
	id := strings.ToLower(f[0])
	flat, reg, err := n.resources(r.Context())
	if err != nil {
		st, msg := n.fail(w, http.StatusBadGateway, err.Error())
		c.Emit(Event{Module: id, Artifact: "metadata", Status: st, Err: msg})
		return
	}
	switch {
	case len(f) == 2 && f[1] == "index.json":
		st, msg := n.passthrough(w, r.Context(), flat+"/"+id+"/index.json", "", id, nugetMetaMax, "application/json")
		if msg != "" {
			c.Emit(Event{Module: id, Artifact: "metadata", Upstream: true, Status: st, Err: msg})
		}
	case len(f) == 3 && nugetVer.MatchString(f[1]) && !strings.Contains(f[1], ".."):
		ver := strings.ToLower(f[1])
		switch f[2] {
		case id + ".nuspec":
			st, msg := n.passthrough(w, r.Context(), flat+"/"+id+"/"+ver+"/"+id+".nuspec", "", id, nugetMetaMax, "application/xml")
			if msg != "" {
				c.Emit(Event{Module: id, Version: ver, Artifact: "metadata", Upstream: true, Status: st, Err: msg})
			}
		case id + "." + ver + ".nupkg":
			a := artifact{Name: id, Version: ver, URL: flat + "/" + id + "/" + ver + "/" + id + "." + ver + ".nupkg"}
			if reg != "" && !n.isPrivate(id) {
				d, err := n.packageHash(r.Context(), reg, id, ver)
				if err != nil {
					var st int
					var msg string
					if nf, ok := err.(errNotFound); ok {
						st, msg = n.notFound(w, id+" "+ver, nf.msg)
					} else {
						st, msg = n.fail(w, http.StatusBadGateway, err.Error())
					}
					c.Emit(Event{Module: id, Version: ver, Artifact: "artifact", Status: st, Err: msg})
					return
				}
				a.Digest = d
			}
			n.serveArtifact(w, r, c, a, n.isPrivate(id))
		default:
			bad("bad flat-container file")
		}
	default:
		bad("bad flat-container path")
	}
}

// packageHash reads id@ver's catalog entry (through its registration leaf)
// for the package hash the feed publishes.
func (n *NuGet) packageHash(ctx context.Context, reg, id, ver string) (Digest, error) {
	b, err := n.meta.get(ctx, n.fetch, reg+"/"+id+"/"+ver+".json", "", nugetMetaMax)
	if err != nil {
		return Digest{}, err
	}
	var leaf struct {
		CatalogEntry string `json:"catalogEntry"`
	}
	if err := json.Unmarshal(b, &leaf); err != nil || leaf.CatalogEntry == "" {
		return Digest{}, fmt.Errorf("%s %s: registration leaf has no catalog entry", id, ver)
	}
	b, err = n.meta.get(ctx, n.fetch, leaf.CatalogEntry, "", nugetMetaMax)
	if err != nil {
		return Digest{}, fmt.Errorf("%s %s: catalog entry: %w", id, ver, err)
	}
	var cat struct {
		Hash string `json:"packageHash"`
		Algo string `json:"packageHashAlgorithm"`
	}
	if err := json.Unmarshal(b, &cat); err != nil || cat.Hash == "" {
		return Digest{}, fmt.Errorf("%s %s: catalog entry has no package hash", id, ver)
	}
	return B64Digest(strings.ToLower(cat.Algo), cat.Hash)
}
