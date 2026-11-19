package keycache

import (
	"path"
	"strings"
)

// alwaysProtected are home paths a cache never holds, hides or sits
// inside, whatever the tool: credentials, git identity, shell start-up
// files and history, and conductor's own files. The agent tools' state and
// login files (agentsandbox.ToolStatePaths) join them through
// Manager.Protected.
var alwaysProtected = []string{
	// credentials and identity
	".ssh", ".gnupg", ".gitconfig", ".config/git", ".netrc", ".git-credentials",
	".aws", ".kube", ".docker", ".config/gh", ".config/hub", ".config/gcloud", ".azure",
	".npmrc", ".yarnrc", ".yarnrc.yml", ".pypirc", ".vault-token", ".gem/credentials",
	".cargo/credentials", ".cargo/credentials.toml", ".m2/settings.xml", ".m2/settings-security.xml",
	".gradle/gradle.properties", ".terraform.d", ".config/rclone", ".password-store",
	"Library/Keychains",
	// credentials kept beside a tool's bulk cache
	".cache/huggingface/token", ".huggingface/token", ".pulumi/credentials.json",
	".android/adbkey", ".android/adbkey.pub", ".config/op", ".op", ".config/glab-cli",
	".local/share/keyrings", ".config/composer/auth.json", ".composer/auth.json",
	".nuget/NuGet/NuGet.Config", ".config/NuGet/NuGet.Config",
	// shell start-up files and history
	".bashrc", ".bash_profile", ".bash_login", ".profile", ".zshrc", ".zshenv", ".zprofile", ".zlogin",
	".config/fish", ".local/share/fish", ".bash_history", ".zsh_history", ".history", ".sh_history",
	".python_history", ".node_repl_history", ".lesshst", ".psql_history", ".mysql_history", ".sqlite_history",
	".irb_history", ".local/state/nvim", ".viminfo",
	// conductor's own
	".tmp", ".conductor",
}

// overlaps reports a == b, or one inside the other (slash paths).
func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// protectedList is alwaysProtected plus extra, cleaned to home-relative
// slash paths.
func protectedList(extra []string) []string {
	out := make([]string, 0, len(alwaysProtected)+len(extra))
	for _, p := range append(append([]string(nil), alwaysProtected...), extra...) {
		p = path.Clean(strings.TrimPrefix(strings.TrimPrefix(p, "~/"), "/"))
		if p != "." && p != "" {
			out = append(out, p)
		}
	}
	return out
}

// protection says how rel (home-relative) meets the protected paths:
// "inside" when it is one or sits inside one (never cached), "holds" when
// one is inside it (cached only below it), "" when it meets none.
func protection(rel string, protected []string) string {
	holds := false
	for _, p := range protected {
		switch {
		case rel == p || strings.HasPrefix(rel, p+"/"):
			return "inside"
		case strings.HasPrefix(p, rel+"/"):
			holds = true
		}
	}
	if holds {
		return "holds"
	}
	return ""
}

// CheckHomePath refuses a cache path no cache may have: not a clean
// relative path in the home, a git directory, a character an overlay mount
// cannot name, or a path that meets a protected one.
func CheckHomePath(rel string, protected []string) bool {
	return plainRel(rel) && protection(rel, protectedList(protected)) == ""
}

// plainRel reports a clean home-relative path an overlay mount can name,
// with no git directory in it.
func plainRel(rel string) bool {
	if rel == "" || rel == "." || strings.HasPrefix(rel, "/") || path.Clean(rel) != rel || strings.ContainsAny(rel, ",:\\\x00\n") {
		return false
	}
	for _, c := range strings.Split(rel, "/") {
		if c == ".." || c == ".git" {
			return false
		}
	}
	return true
}
