package hostcmd

import (
	"fmt"
	"strings"
)

// Content-executing host commands (#154). Some host commands run what the
// agent wrote: `terraform plan` runs the configuration's providers and data
// sources (an `external` data source is any program), `docker build` the
// Dockerfile's RUN steps, `kubectl kustomize` its plugins, `npm publish` the
// workspace's packing under the operator's registry login. On the host they
// would run outside the jail, as the operator, with the operator's
// credentials. So they are refused unless the operator's own configuration
// names them in the binary's allow list — and one that is allowed runs in a
// host-side jail (Decision.Confine): the workspace read-only (its writes
// copy-on-write), only the tool's own config paths from $HOME, no stdin and
// no terminal, and the network limited to the tool's service endpoints
// (ServiceEndpoints, or the binary's own `network:` block).

// contentExec reports whether p executes workspace content: why it does (for
// the refusal) and the allow-list pattern that would permit it. "" = no.
func contentExec(tool string, p Parsed, ctx Context) (why, knob string) {
	w := p.Words
	word := func(i int) string {
		if i < len(w) {
			return w[i]
		}
		return ""
	}
	path := func(n int) string { return strings.Join(w[:min(n, len(w))], " ") }
	switch tool {
	case "terraform":
		switch word(0) {
		case "init", "get":
			return "it downloads the module sources and provider plugins the workspace's configuration names, and runs git for them as you", word(0) + " *"
		case "plan", "apply", "destroy", "refresh", "import", "console", "test":
			return "it runs the workspace configuration's provider plugins and data sources — an `external` data source is any program", word(0) + " *"
		case "show", "validate":
			return "it loads the provider plugins under .terraform, which the agent can write", word(0) + " *"
		case "providers", "state":
			if word(1) == "schema" || word(1) == "show" {
				return "it loads the provider plugins under .terraform, which the agent can write", path(2) + " *"
			}
		}
	case "docker":
		verb, at := word(0), 1
		if verb == "image" || verb == "builder" || verb == "container" || verb == "buildx" {
			verb, at = word(0)+" "+word(1), 2
		}
		switch verb {
		case "build", "image build", "builder build", "buildx build", "buildx bake":
			return "it runs the workspace's Dockerfile steps on your Docker daemon", verb + " *"
		case "run", "create", "container run", "container create":
			if img := word(at); img != "" && ctx.LocalImage != nil && ctx.LocalImage(img) {
				return fmt.Sprintf("image %s was built on this machine (possibly from workspace content), and it runs on your Docker daemon", img), verb + " *"
			}
		}
	case "kubectl":
		switch word(0) {
		case "apply", "create", "replace":
			if len(p.Flags["--filename"]) > 0 || len(p.Flags["--kustomize"]) > 0 {
				return "it applies manifests from the workspace to your cluster (and kustomize can run plugins)", word(0) + " *"
			}
		case "kustomize":
			return "kustomize can run plugins and helm from the workspace's kustomization", "kustomize *"
		}
	case "aws":
		switch word(0) + " " + word(1) {
		case "cloudformation deploy", "cloudformation package", "cloudformation create-stack", "cloudformation update-stack",
			"cloudformation create-change-set", "cloudformation create-stack-set", "cloudformation update-stack-set":
			return "it deploys a template from the workspace into your account", path(2) + " *"
		case "lambda create-function", "lambda update-function-code", "lambda publish-layer-version":
			return "it uploads code from the workspace to run in your account", path(2) + " *"
		}
	case "gcloud":
		for _, c := range []string{"builds submit", "functions deploy", "run deploy", "app deploy"} {
			if strings.HasPrefix(strings.Join(w, " ")+" ", c+" ") {
				return "it uploads workspace content to build or run in your project", c + " *"
			}
		}
	case "az":
		switch {
		case word(0) == "deployment":
			return "it deploys a template from the workspace into your subscription", "deployment *"
		case word(0) == "functionapp" && word(1) == "deployment", word(0) == "webapp" && (word(1) == "deploy" || word(1) == "up"):
			return "it uploads workspace content to run in your subscription", path(2) + " *"
		}
	case "npm", "pnpm":
		if word(0) == "publish" {
			return "it packs and publishes the workspace under your registry login, reading the workspace's .npmrc", "publish *"
		}
	}
	return "", ""
}

// explicitlyAllowed reports whether the operator's own configuration — a
// global or runtime layer, never a step (a step can only narrow) — names this
// command: an allow pattern that matches it and whose first word is literal,
// so a blanket `*` does not count.
func explicitlyAllowed(rule Rule, p Parsed) bool {
	for _, pat := range rule.OperatorAllow {
		f := strings.Fields(pat)
		if len(f) == 0 || strings.ContainsAny(f[0], "*?[") || strings.HasPrefix(f[0], "-") {
			continue
		}
		if MatchRule(pat, p.Words, p.Flags) {
			return true
		}
	}
	return false
}

// serviceEndpoints are each tool's own service endpoints: the network a
// confined (content-executing) run may reach by default. A binary's own
// `network:` block replaces them.
var serviceEndpoints = map[string][]string{
	"terraform": {"registry.terraform.io", "releases.hashicorp.com", "app.terraform.io",
		"github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com",
		"*.amazonaws.com", "*.googleapis.com", "management.azure.com", "login.microsoftonline.com"},
	"docker": {"registry-1.docker.io", "auth.docker.io", "index.docker.io", "production.cloudflare.docker.com",
		"ghcr.io", "pkg-containers.githubusercontent.com", "*.gcr.io", "*.pkg.dev", "*.amazonaws.com",
		"*.azurecr.io", "mcr.microsoft.com"},
	"kubectl": {"*.amazonaws.com", "*.googleapis.com", "login.microsoftonline.com"},
	"aws":     {"*.amazonaws.com", "*.aws.amazon.com"},
	"gcloud":  {"*.googleapis.com", "accounts.google.com"},
	"az":      {"management.azure.com", "login.microsoftonline.com", "*.azure.com", "*.windows.net"},
	"npm":     {"registry.npmjs.org"},
	"pnpm":    {"registry.npmjs.org"},
}

// ServiceEndpoints is the default network allowlist of tool's confined runs.
func ServiceEndpoints(tool string) []string {
	return append([]string(nil), serviceEndpoints[tool]...)
}
