package fixer

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BrowserProbeMarker in a prompt makes the fake agent drive conductor's
// browser (group BR) and record each verdict in BROWSER_PROBE.md in its
// workspace. "[[browser-probe operator]]" also signs in with the test
// site's credential — which only an operator's own request may.
const BrowserProbeMarker = "[[browser-probe"

const site = "http://localhost:8090"

// BrowserProbe runs the probe in dir.
func BrowserProbe(dir, prompt string) {
	if !strings.Contains(prompt, BrowserProbeMarker) {
		return
	}
	operator := strings.Contains(prompt, "[[browser-probe operator]]")
	var b strings.Builder
	line := func(k, v string) { fmt.Fprintf(&b, "%s: %s\n", k, strings.ReplaceAll(v, "\n", " ")) }
	br := func(args ...string) (map[string]any, string, error) {
		out, err := exec.Command("conductor", append([]string{"browser"}, args...)...).CombinedOutput()
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		return m, string(out), err
	}
	verdict := func(out string, err error) string {
		if err == nil {
			return "ok"
		}
		return "refused: " + probeLine(strings.TrimSpace(out))
	}
	snap := func() string { _, out, _ := br("snapshot"); return out }
	ref := func(s string, want ...string) string {
		for _, l := range strings.Split(s, "\n") {
			ok := true
			for _, w := range want {
				ok = ok && strings.Contains(l, w)
			}
			if i := strings.Index(l, "[ref="); ok && i >= 0 {
				r := l[i+5:]
				return r[:strings.IndexAny(r, "],")]
			}
		}
		return "none"
	}

	line("kind", map[bool]string{true: "operator", false: "event"}[operator])
	_ = os.WriteFile(filepath.Join(dir, "upload-me.txt"), []byte("from the workspace"), 0o644)
	_, out, err := br("open", site+"/form")
	line("open", verdict(out, err))
	s := snap()
	_, out, err = br("fill", ref(s, "textbox", "Name"), "E2E Agent")
	line("fill", verdict(out, err))
	_, out, err = br("select", ref(s, "Color"), "Red")
	line("select", verdict(out, err))
	_, out, err = br("upload", ref(s, "Attachment"), "upload-me.txt")
	line("upload", verdict(out, err))
	_, out, err = br("upload", ref(s, "Attachment"), "../../../etc/passwd")
	line("upload-escape", verdict(out, err))
	m, out, err := br("click", ref(s, "button", "Submit"))
	line("submit", fmt.Sprintf("%s %v", verdict(out, err), m["title"]))

	_, _, _ = br("open", site+"/")
	_, out, err = br("click", ref(snap(), "link", "Download report"))
	got := ""
	for i := 0; i < 30 && got == ""; i++ {
		m, _, _ := br("downloads")
		if ds, ok := m["downloads"].([]any); ok {
			for _, d := range ds {
				if p, _ := d.(map[string]any)["path"].(string); p != "" {
					got = p
				}
			}
		}
	}
	line("download", got)
	if b, err := os.ReadFile(filepath.Join(dir, got)); err == nil && strings.HasPrefix(string(b), "id,item,qty") {
		line("download-content", "ok")
	} else {
		line("download-content", "missing")
	}
	m, out, err = br("screenshot", "--path", "shot.png")
	if b, rerr := os.ReadFile(filepath.Join(dir, "shot.png")); err == nil && rerr == nil && strings.HasPrefix(string(b), "\x89PNG") {
		line("screenshot", "ok")
	} else {
		line("screenshot", verdict(out, err))
	}
	_, out, err = br("open", "https://example.com/")
	line("offlist", verdict(out, err))
	_, out, err = br("evaluate", "document.title")
	line("evaluate", verdict(out, err))

	m, _, _ = br("open", site+"/account")
	line("profile", fmt.Sprint(m["profile"]))
	line("logged-in-note", fmt.Sprint(m["logged_in_note"]))
	if st, _, _ := br("status"); st != nil {
		line("identity", fmt.Sprint(st["identity"]))
	}
	if strings.Contains(snap(), "Signed in as") {
		line("account", "signed-in")
	} else {
		line("account", "anonymous")
	}
	m, out, err = br("login", "--credential", "testsite")
	line("login", fmt.Sprintf("%s %v", verdict(out, err), m["steps"]))
	if strings.Contains(snap(), "Signed in as") {
		line("after-login", "signed-in")
	} else {
		line("after-login", "anonymous")
	}
	if operator {
		_, _, _ = br("open", "http://127.0.0.1:8090/login")
		s := snap()
		_, out, err = br("fill-credential", ref(s, "textbox", "Password"), "password", "--credential", "testsite")
		line("lookalike", verdict(out, err))
	}
	_ = os.WriteFile(filepath.Join(dir, "BROWSER_PROBE.md"), []byte(b.String()), 0o644)
}

// probeLine is the first line of a reply, long enough to hold the reason.
func probeLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return strings.TrimSpace(s)
}
