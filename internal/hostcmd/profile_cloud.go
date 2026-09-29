package hostcmd

import "strings"

// Cloud CLIs: aws, gcloud, az. Each parses to service + operation (aws) or
// the command group path (gcloud/az), refuses the credential surface by name
// (#154 §3.1), and names the cache paths that must persist through the
// copy-on-write home (rotating tokens).

// ---- aws -------------------------------------------------------------------

var awsSpec = flagSpec{
	value: set("--region", "--profile", "--output", "--endpoint-url", "--query",
		"--cli-read-timeout", "--cli-connect-timeout", "--color", "--ca-bundle",
		"--cli-binary-format", "--include", "--exclude", "--acl", "--storage-class",
		"--content-type", "--sse", "--sse-kms-key-id", "--expires", "--metadata",
		"--grants", "--cache-control", "--page-size", "--cli-input-json", "--cli-input-yaml"),
	boolean: set("--debug", "--no-verify-ssl", "--no-paginate", "--no-sign-request",
		"--no-cli-pager", "--cli-auto-prompt", "--no-cli-auto-prompt", "--recursive",
		"--dryrun", "--quiet", "--only-show-errors", "--no-progress", "--follow-symlinks",
		"--no-follow-symlinks", "--exact-timestamps", "--delete", "--size-only",
		"--no-guess-mime-type", "--human-readable", "--summarize", "--force",
		"--generate-cli-skeleton"),
	unknownTakesValue: true,
}

type awsProfile struct{}

// awsRefused are service+operation pairs that mint, print, or change
// credentials. "*" covers the whole service.
var awsRefused = map[string]map[string]bool{
	"configure":    set("*"),
	"sso":          set("login", "logout"),
	"sso-oidc":     set("*"),
	"login":        set("*"),
	"logout":       set("*"),
	"sts":          set("get-session-token", "assume-role", "assume-role-with-saml", "assume-role-with-web-identity", "get-federation-token", "assume-root"),
	"ecr":          set("get-login-password", "get-authorization-token", "get-login"),
	"ecr-public":   set("get-login-password", "get-authorization-token"),
	"codeartifact": set("get-authorization-token", "login"),
	"iam":          set("create-access-key", "update-access-key", "create-login-profile", "update-login-profile", "create-service-specific-credential", "reset-service-specific-credential"),
	"eks":          set("get-token"),
	"rds":          set("generate-db-auth-token"),
}

func (awsProfile) parse(args []string, _ Context) Parsed {
	pos, flags := parseFlags(args, awsSpec)
	p := Parsed{Words: pos, Flags: flags}
	if p.has("--cli-auto-prompt") {
		p.Refuse = "built-in: aws --cli-auto-prompt (interactive)"
		return p
	}
	for _, k := range []string{"--cli-input-json", "--cli-input-yaml", "--ca-bundle"} {
		for _, v := range flags[k] {
			p.PathArgs = append(p.PathArgs, v)
		}
	}
	svc, op := p.word(0), p.word(1)
	if ops, ok := awsRefused[svc]; ok && (ops["*"] || ops[op]) {
		p.Refuse = "built-in: aws " + svc + " " + firstNonEmptyStr(op, "*")
	}
	return p
}

// ---- gcloud ----------------------------------------------------------------

var gcloudSpec = flagSpec{
	value: set("--project", "--account", "--configuration", "--format", "--filter",
		"--limit", "--sort-by", "--region", "--zone", "--impersonate-service-account",
		"--verbosity", "--flags-file", "--billing-project"),
	boolean:           set("--quiet", "--no-user-output-enabled", "--log-http"),
	unknownTakesValue: true,
}

type gcloudProfile struct{}

func (gcloudProfile) parse(args []string, _ Context) Parsed {
	pos, flags := parseFlags(args, gcloudSpec)
	p := Parsed{Words: pos, Flags: flags}
	for _, v := range flags["--flags-file"] {
		p.PathArgs = append(p.PathArgs, v)
	}
	path := strings.Join(pos, " ")
	for _, r := range []string{
		"auth", "config set", "config unset", "config configurations",
		"iam service-accounts keys create", "iam service-accounts keys upload",
		"init", "components install", "components update", "components remove",
	} {
		if path == r || strings.HasPrefix(path, r+" ") {
			p.Refuse = "built-in: gcloud " + r + " *"
			return p
		}
	}
	// print-access-token / print-identity-token under any group.
	for _, w := range pos {
		if strings.HasPrefix(w, "print-") && strings.HasSuffix(w, "-token") {
			p.Refuse = "built-in: gcloud * " + w
			return p
		}
	}
	return p
}

// ---- az --------------------------------------------------------------------

var azSpec = flagSpec{
	value: set("--subscription", "--output", "--query", "--resource-group", "--name",
		"--location", "--only-show-errors"),
	alias:             map[string]string{"-o": "--output", "-g": "--resource-group", "-n": "--name", "-l": "--location"},
	boolean:           set("--debug", "--verbose", "--yes"),
	unknownTakesValue: true,
}

type azProfile struct{}

func (azProfile) parse(args []string, _ Context) Parsed {
	pos, flags := parseFlags(args, azSpec)
	p := Parsed{Words: pos, Flags: flags}
	path := strings.Join(pos, " ")
	for _, r := range []string{
		"login", "logout", "account set", "account clear", "account get-access-token",
		"ad sp create-for-rbac", "ad sp credential reset", "ad app credential reset",
		"config set", "config unset", "extension add", "extension update", "upgrade",
	} {
		if path == r || strings.HasPrefix(path, r+" ") {
			p.Refuse = "built-in: az " + r
			return p
		}
	}
	return p
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func init() {
	register("aws", awsProfile{}, homeView{
		Paths:   []string{".aws"},
		Persist: []string{".aws/sso/cache"},
		Env:     map[string]string{"AWS_PAGER": "", "AWS_CLI_AUTO_PROMPT": "off"},
	})
	register("gcloud", gcloudProfile{}, homeView{
		Paths:   []string{".config/gcloud", ".boto"},
		Persist: []string{".config/gcloud/access_tokens.db"},
		Env:     map[string]string{"CLOUDSDK_CORE_DISABLE_PROMPTS": "1"},
	})
	register("az", azProfile{}, homeView{
		Paths:   []string{".azure"},
		Persist: []string{".azure/msal_token_cache.json", ".azure/msal_token_cache.bin"},
		Env:     map[string]string{"AZURE_CORE_NO_COLOR": "1", "AZURE_CORE_ONLY_SHOW_ERRORS": "1"},
	})
}
