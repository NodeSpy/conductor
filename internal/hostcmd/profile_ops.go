package hostcmd

import (
	"path/filepath"
	"strings"
)

// Operations tools: kubectl, docker, terraform, ssh/scp, npm/pnpm.

// ---- kubectl ---------------------------------------------------------------

var kubectlSpec = flagSpec{
	value: set("--namespace", "--context", "--cluster", "--user", "--kubeconfig",
		"--server", "--token", "--as", "--as-group", "--as-uid", "--output", "--selector",
		"--filename", "--container", "--field-selector", "--request-timeout", "--v",
		"--template", "--sort-by", "--label-columns", "--type", "--image", "--replicas",
		"--port", "--timeout", "--patch", "--cascade", "--grace-period", "--since",
		"--tail", "--from-literal", "--from-file", "--from-env-file", "--certificate-authority",
		"--client-certificate", "--client-key", "--username", "--password", "--kustomize",
		"--overrides", "--env", "--limits", "--requests", "--dry-run", "--subresource"),
	alias: map[string]string{
		"-n": "--namespace", "-s": "--server", "-o": "--output", "-l": "--selector",
		"-f": "--filename", "-c": "--container", "-v": "--v", "-k": "--kustomize",
		"-p": "--patch", "-i": "--stdin", "-t": "--tty", "-A": "--all-namespaces",
		"-w": "--watch", "-R": "--recursive",
	},
}

// kubeResources canonicalizes the common resource spellings so a rule
// `delete deployments *` also catches `delete deploy/api`.
var kubeResources = map[string]string{
	"po": "pods", "pod": "pods", "deploy": "deployments", "deployment": "deployments",
	"svc": "services", "service": "services", "ns": "namespaces", "namespace": "namespaces",
	"cm": "configmaps", "configmap": "configmaps", "secret": "secrets", "ing": "ingresses",
	"ingress": "ingresses", "sts": "statefulsets", "statefulset": "statefulsets",
	"ds": "daemonsets", "daemonset": "daemonsets", "rs": "replicasets", "replicaset": "replicasets",
	"job": "jobs", "cj": "cronjobs", "cronjob": "cronjobs", "pvc": "persistentvolumeclaims",
	"persistentvolumeclaim": "persistentvolumeclaims", "pv": "persistentvolumes",
	"persistentvolume": "persistentvolumes", "sa": "serviceaccounts", "serviceaccount": "serviceaccounts",
	"no": "nodes", "node": "nodes", "ev": "events", "event": "events", "hpa": "horizontalpodautoscalers",
	"netpol": "networkpolicies", "crd": "customresourcedefinitions",
}

// kubeSubVerbs have a sub-command before the resource.
var kubeSubVerbs = set("rollout", "config", "auth", "certificate", "set", "top", "create", "apply", "cluster-info", "plugin", "alpha")

type kubectlProfile struct{}

func (kubectlProfile) parse(args []string, _ Context) Parsed {
	pos, flags := parseFlags(args, kubectlSpec)
	p := Parsed{Flags: flags}
	for _, k := range []string{"--kubeconfig", "--filename", "--from-file", "--from-env-file", "--certificate-authority", "--client-certificate", "--client-key", "--kustomize"} {
		for _, v := range flags[k] {
			if v != "-" {
				p.PathArgs = append(p.PathArgs, strings.TrimPrefix(v, "="))
			}
		}
	}
	// Canonical words: verb [sub] resource name…, with type/name split and
	// the resource type canonicalized.
	var words []string
	resAt := 1
	for i, w := range pos {
		if i == 0 && kubeSubVerbs[w] {
			resAt = 2
		}
		if i == resAt {
			if t, n, ok := strings.Cut(w, "/"); ok {
				words = append(words, kubeCanon(t), n)
				continue
			}
			words = append(words, kubeCanon(w))
			continue
		}
		words = append(words, w)
	}
	p.Words = words
	verb, sub := p.word(0), p.word(1)
	switch verb {
	case "config":
		switch {
		case strings.HasPrefix(sub, "set"), strings.HasPrefix(sub, "delete"), sub == "use-context",
			sub == "use", sub == "rename-context", sub == "unset":
			p.Refuse = "built-in: kubectl config " + sub
		case sub == "view" && p.has("--raw"):
			p.Refuse = "built-in: kubectl config view --raw"
		}
	case "proxy", "port-forward":
		p.Refuse = "built-in: kubectl " + verb + " (opens a listener on the host)"
	case "plugin":
		p.Refuse = "built-in: kubectl plugin *"
	case "create":
		if sub == "token" {
			p.Refuse = "built-in: kubectl create token (mints a credential)"
		}
	}
	return p
}

func kubeCanon(t string) string {
	t = strings.ToLower(t)
	base, group, _ := strings.Cut(t, ".")
	if c, ok := kubeResources[base]; ok {
		base = c
	}
	if group != "" {
		return base + "." + group
	}
	return base
}

// ---- docker ----------------------------------------------------------------

var dockerSpec = flagSpec{
	value: set("--volume", "--mount", "--env", "--env-file", "--name", "--network", "--net",
		"--publish", "--workdir", "--user", "--entrypoint", "--label", "--label-file",
		"--pid", "--ipc", "--uts", "--userns", "--cap-add", "--cap-drop", "--device",
		"--security-opt", "--volumes-from", "--tag", "--file", "--build-arg", "--target",
		"--platform", "--secret", "--ssh", "--output", "--cache-from", "--cache-to",
		"--host", "--context", "--config", "--log-level", "--memory", "--cpus",
		"--restart", "--hostname", "--add-host", "--dns", "--format", "--filter", "--iidfile",
		"--cidfile", "--allow", "--attach", "--gpus", "--tmpfs", "--shm-size", "--ulimit",
		"--group-add", "--cgroup-parent", "--cgroupns", "--runtime", "--sysctl", "--project-directory",
		"--project-name", "--profile", "--env-file"),
	alias: map[string]string{
		"-v": "--volume", "-e": "--env", "-p": "--publish", "-w": "--workdir", "-u": "--user",
		"-l": "--label", "-t": "--tag", "-f": "--file", "-H": "--host", "-c": "--context",
		"-o": "--output", "-a": "--attach", "-i": "--interactive", "-d": "--detach",
	},
}

// dockerRunLike are the sub-commands that start a container.
var dockerRunLike = set("run", "create")

type dockerProfile struct{}

func (dockerProfile) parse(args []string, ctx Context) Parsed {
	pos, flags := parseFlags(args, dockerSpec)
	p := Parsed{Words: pos, Flags: flags}
	for _, k := range []string{"--env-file", "--label-file", "--file", "--iidfile", "--cidfile", "--config", "--output"} {
		for _, v := range flags[k] {
			if v != "-" {
				p.PathArgs = append(p.PathArgs, strings.TrimPrefix(v, "dest="))
			}
		}
	}
	cmd, sub := p.word(0), p.word(1)
	switch cmd {
	case "login", "logout":
		p.Refuse = "built-in: docker " + cmd
		return p
	case "context":
		p.Refuse = "built-in: docker context *"
		return p
	case "plugin":
		if sub != "ls" && sub != "inspect" {
			p.Refuse = "built-in: docker plugin " + sub
			return p
		}
	case "compose":
		// A compose file the agent writes can bind any host path; only the
		// read-only compose verbs reach the host.
		if !set("ps", "ls", "logs", "config", "version", "images", "top", "events", "port")[sub] {
			p.Refuse = "built-in: docker compose " + sub + " (a compose file can mount the host)"
			return p
		}
	case "container":
		if dockerRunLike[sub] {
			cmd = sub
		}
	case "buildx", "builder":
		if a := flags["--allow"]; len(a) > 0 {
			p.Refuse = "built-in: docker buildx --allow (entitlements)"
			return p
		}
	}
	if len(flags["--ssh"]) > 0 {
		p.Refuse = "built-in: docker build --ssh (forwards the operator's ssh agent)"
		return p
	}
	for _, s := range flags["--secret"] {
		for _, part := range strings.Split(s, ",") {
			if k, v, ok := strings.Cut(part, "="); ok && (k == "src" || k == "source") {
				p.PathArgs = append(p.PathArgs, v)
			}
		}
	}
	if dockerRunLike[cmd] || cmd == "run" || cmd == "create" {
		if r := dockerHostAccess(flags, ctx); r != "" {
			p.Refuse = r
			return p
		}
	}
	if cmd == "cp" {
		// docker cp CONTAINER:SRC DEST / SRC CONTAINER:DEST — the local side
		// is a host path.
		for _, a := range pos[1:] {
			if !strings.Contains(a, ":") {
				p.PathArgs = append(p.PathArgs, a)
			}
		}
	}
	return p
}

// dockerHostAccess refuses the container options that reach the host:
// binding a host path outside the workspace (`-v /:/host`, a mount of
// type=bind), privileged mode, extra capabilities, devices, and the host's
// pid/ipc/uts/userns/network namespaces.
func dockerHostAccess(flags map[string][]string, ctx Context) string {
	if _, ok := flags["--privileged"]; ok {
		return "built-in: docker run --privileged"
	}
	for _, k := range []string{"--cap-add", "--device", "--security-opt", "--volumes-from", "--cgroup-parent", "--runtime", "--gpus"} {
		if len(flags[k]) > 0 {
			return "built-in: docker run " + k
		}
	}
	for _, k := range []string{"--pid", "--ipc", "--uts", "--userns", "--network", "--net", "--cgroupns"} {
		for _, v := range flags[k] {
			if v == "host" || strings.HasPrefix(v, "container:") {
				return "built-in: docker run " + k + "=" + v
			}
		}
	}
	bindOK := func(src string) bool {
		if !strings.HasPrefix(src, "/") && !strings.HasPrefix(src, ".") && !strings.HasPrefix(src, "~") {
			return true // a named volume
		}
		abs := resolveArgPath(src, ctx.Workspace, ctx.Home)
		within := func(root string) bool {
			if root == "" {
				return false
			}
			root = filepath.Clean(root)
			return abs == root || strings.HasPrefix(abs, root+"/")
		}
		return within(ctx.Workspace) || within(ctx.TmpDir)
	}
	for _, v := range flags["--volume"] {
		src, _, _ := strings.Cut(v, ":")
		if !bindOK(src) {
			return "built-in: docker run -v " + v + " (mounts the host outside the workspace)"
		}
	}
	for _, m := range flags["--mount"] {
		typ, src := "volume", ""
		for _, part := range strings.Split(m, ",") {
			k, v, _ := strings.Cut(part, "=")
			switch k {
			case "type":
				typ = v
			case "source", "src":
				src = v
			}
		}
		if typ == "bind" && !bindOK(src) {
			return "built-in: docker run --mount " + m + " (mounts the host outside the workspace)"
		}
	}
	return ""
}

// ---- terraform -------------------------------------------------------------

type terraformProfile struct{}

func (terraformProfile) parse(args []string, _ Context) Parsed {
	var pos []string
	flags := map[string][]string{}
	var paths []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			n, v, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
			flags["--"+n] = append(flags["--"+n], v)
			if n == "chdir" || n == "var-file" || n == "state" || n == "state-out" || n == "backup" || n == "plugin-dir" || n == "out" {
				paths = append(paths, v)
			}
			continue
		}
		pos = append(pos, a)
	}
	p := Parsed{Words: pos, Flags: flags, PathArgs: paths}
	switch p.word(0) {
	case "login", "logout":
		p.Refuse = "built-in: terraform " + p.word(0)
	case "console":
		p.Refuse = "built-in: terraform console (interactive)"
	}
	return p
}

// ---- ssh / scp -------------------------------------------------------------

var sshSpec = flagSpec{
	value: set("-b", "-c", "-D", "-E", "-e", "-F", "-I", "-i", "-J", "-L", "-l", "-m", "-O", "-o",
		"-p", "-Q", "-R", "-S", "-W", "-w", "-B", "-P"),
}

// sshLocalExec are the ssh options that run a command on THIS host.
var sshLocalExec = set("proxycommand", "localcommand", "permitlocalcommand", "knownhostscommand",
	"proxyusefdpass", "include", "match", "controlpath", "controlmaster", "forwardagent",
	"identityagent", "pkcs11provider", "securitykeyprovider", "remotecommand", "localforward",
	"remoteforward", "dynamicforward", "tunnel", "tunneldevice", "sessiontype", "stdinnull",
	"forkafterauthentication")

type sshProfile struct{ scp bool }

func (s sshProfile) parse(args []string, _ Context) Parsed {
	pos, flags := parseFlags(args, sshSpec)
	p := Parsed{Words: pos, Flags: flags}
	tool := "ssh"
	if s.scp {
		tool = "scp"
	}
	for _, o := range flags["-o"] {
		k, _, _ := strings.Cut(o, "=")
		k = strings.ToLower(strings.TrimSpace(strings.Fields(k + " ")[0]))
		if sshLocalExec[k] {
			p.Refuse = "built-in: " + tool + " -o " + k
			return p
		}
	}
	for _, f := range []string{"-F", "-S", "-M", "-A", "-L", "-R", "-D", "-w", "-O", "-f", "-I"} {
		if _, ok := flags[f]; ok {
			p.Refuse = "built-in: " + tool + " " + f
			return p
		}
	}
	for _, v := range flags["-i"] {
		p.PathArgs = append(p.PathArgs, v)
	}
	if s.scp {
		for _, a := range pos {
			host, rpath, remote := strings.Cut(a, ":")
			if at := strings.LastIndexByte(host, '@'); at >= 0 {
				host = host[at+1:]
			}
			if remote && !strings.Contains(host, "/") {
				if gitHost(host) {
					p.Refuse = "built-in: scp to a git host"
					return p
				}
				_ = rpath
				continue
			}
			p.PathArgs = append(p.PathArgs, a)
		}
		return p
	}
	if len(pos) > 0 {
		dest := pos[0]
		if strings.HasPrefix(dest, "ssh://") {
			dest = strings.TrimPrefix(dest, "ssh://")
			dest, _, _ = strings.Cut(dest, "/")
		}
		if at := strings.LastIndexByte(dest, '@'); at >= 0 {
			dest = dest[at+1:]
		}
		dest, _, _ = strings.Cut(dest, ":")
		if gitHost(dest) {
			p.Refuse = "built-in: ssh to " + dest + " (git goes through conductor's remote helper)"
			return p
		}
		for _, w := range pos[1:] {
			if strings.HasPrefix(w, "git-receive-pack") || strings.HasPrefix(w, "git-upload-pack") || strings.HasPrefix(w, "git-upload-archive") {
				p.Refuse = "built-in: ssh " + w + " (git goes through conductor's remote helper)"
				return p
			}
		}
	}
	return p
}

// gitHost reports the forges whose git traffic must use conductor's remote
// helper — a raw `ssh git@github.com git-receive-pack` would push around it.
func gitHost(h string) bool {
	h = strings.ToLower(h)
	return h == "github.com" || h == "ssh.github.com" || strings.HasSuffix(h, ".github.com") ||
		h == "gitlab.com" || h == "bitbucket.org"
}

// ---- npm / pnpm ------------------------------------------------------------

// npmHost are the npm verbs that need the operator's registry credentials —
// the only ones that run on the host. Everything else (install, run, test,
// ci, exec, …) runs natively in the jail.
var npmHost = set("publish", "unpublish", "deprecate", "dist-tag", "owner", "access", "whoami", "ping", "star", "unstar", "team", "org", "hook")

// npmRefused are the credential-changing verbs (#154 §3.1).
var npmRefused = set("login", "logout", "adduser", "add-user", "token", "set-script")

type npmProfile struct{ tool string }

func (n npmProfile) parse(args []string, _ Context) Parsed {
	var pos []string
	flags := map[string][]string{}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			k, v, _ := strings.Cut(a, "=")
			flags[k] = append(flags[k], v)
			continue
		}
		pos = append(pos, a)
	}
	p := Parsed{Words: pos, Flags: flags}
	verb := p.word(0)
	switch {
	case npmRefused[verb]:
		p.Refuse = "built-in: " + n.tool + " " + verb
	case verb == "config" || verb == "c":
		if s := p.word(1); s != "get" && s != "list" && s != "ls" {
			p.Refuse = "built-in: " + n.tool + " config " + s
		}
	case npmHost[verb]:
		if verb == "publish" {
			// Lifecycle scripts (prepublishOnly, …) are agent-written code;
			// on the host they would run outside the jail.
			p.ExtraArgs = []string{"--ignore-scripts"}
		}
	default:
		p.Native = true
	}
	return p
}

func init() {
	register("kubectl", kubectlProfile{}, homeView{
		Paths:   []string{".kube", ".aws", ".config/gcloud", ".azure"},
		Persist: []string{".kube/cache"},
	})
	register("docker", dockerProfile{}, homeView{Paths: []string{".docker"}})
	register("terraform", terraformProfile{}, homeView{
		Paths:   []string{".terraform.d", ".terraformrc", ".aws", ".config/gcloud", ".azure", ".kube"},
		Persist: []string{".terraform.d/plugin-cache"},
		Env:     map[string]string{"TF_INPUT": "0", "TF_IN_AUTOMATION": "1", "CHECKPOINT_DISABLE": "1"},
	})
	register("ssh", sshProfile{}, homeView{Paths: []string{".ssh"}, Env: map[string]string{}})
	register("scp", sshProfile{scp: true}, homeView{Paths: []string{".ssh"}})
	register("npm", npmProfile{tool: "npm"}, homeView{Paths: []string{".npmrc", ".npm/_cacache"}})
	register("pnpm", npmProfile{tool: "pnpm"}, homeView{Paths: []string{".npmrc", ".config/pnpm"}})
}
