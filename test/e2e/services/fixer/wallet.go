package fixer

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// WalletProbeMarker in a prompt makes the fake agent use conductor's wallet
// (group PAY): sign up at the fake shop from a profile, propose purchases,
// wait for the operator's (the harness's) signed approval, pay — and try a
// second use, a price bump, an over-cap total and another merchant. Each
// verdict lands in WALLET_PROBE.md in its workspace.
const WalletProbeMarker = "[[wallet-probe]]"

const (
	shop      = "http://localhost:8091"
	otherShop = "http://127.0.0.1:8092"
)

// WalletProbe runs the probe in dir.
func WalletProbe(dir, prompt string) {
	if !strings.Contains(prompt, WalletProbeMarker) {
		return
	}
	var b strings.Builder
	line := func(k, v string) {
		fmt.Fprintf(&b, "%s: %s\n", k, strings.ReplaceAll(v, "\n", " "))
		_ = os.WriteFile(filepath.Join(dir, "WALLET_PROBE.md"), []byte(b.String()), 0o644)
	}
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
	// waitFor polls a purchase until it leaves pending (the harness decides).
	waitFor := func(id string) string {
		for i := 0; i < 120; i++ {
			m, _, _ := br("purchase-status", id)
			if s, _ := m["status"].(string); s != "" && s != "pending" {
				return s
			}
			time.Sleep(time.Second)
		}
		return "still pending"
	}
	propose := func(total, merchant string) (string, string) {
		m, out, err := br("purchase", "--card", "e2ecard", "--merchant", merchant, "--total", total, "--currency", "USD",
			"--items", `[{"name":"Widget","qty":1,"amount":12}]`, "--why", "the e2e wants a widget")
		if err != nil {
			return "", verdict(out, err)
		}
		id, _ := m["purchase"].(string)
		return id, fmt.Sprint(m["status"])
	}
	checkout := func() string {
		_, _, _ = br("open", shop+"/checkout")
		return ref(snap(), "button", "Place order")
	}

	// A sign-up from the profile: the agent never sees a value.
	m, _, _ := br("profiles")
	fb, _ := json.Marshal(m["profiles"])
	line("facts", string(fb))
	_, _, _ = br("open", shop+"/signup")
	m, out, err := br("fill-form", "--profile", "shopper")
	line("fill-form", fmt.Sprintf("%s %v", verdict(out, err), m["filled"]))
	form := snap()
	line("form-shows-mark", fmt.Sprint(strings.Contains(form, "filled by conductor")))
	_, out, err = br("click", ref(form, "button", "Sign up"))
	line("signup", verdict(out, err))
	welcome := snap()
	line("welcome-scrubbed", fmt.Sprint(strings.Contains(welcome, "Welcome") && strings.Contains(welcome, "[redacted]")))

	// A purchase: proposed, approved by the operator, paid, a receipt.
	_, _, _ = br("open", shop+"/cart/add?item=widget")
	id, st := propose("12.00", shop)
	line("propose", st)
	place := checkout()
	_, out, err = br("pay", id, place)
	line("pay-before-approval", verdict(out, err))
	line("decision", waitFor(id))
	place = checkout()
	m, out, err = br("pay", id, place)
	line("pay", fmt.Sprintf("%s %v %v", verdict(out, err), m["order_number"], m["charged"]))
	conf := snap()
	line("confirmation-scrubbed", fmt.Sprint(strings.Contains(conf, "FS-") && !strings.Contains(conf, "4242 4242")))
	_, _, _ = br("open", shop+"/cart/add?item=widget")
	place = checkout()
	_, out, err = br("pay", id, place)
	line("second-use", verdict(out, err))

	// A price bump at checkout (the harness raises it before approving).
	id2, st := propose("12.00", shop)
	line("propose2", st)
	line("decision2", waitFor(id2))
	place = checkout()
	_, out, err = br("pay", id2, place)
	line("price-bump", verdict(out, err))

	// Over the per-purchase limit; a merchant the card does not allow.
	_, st = propose("31.00", shop)
	line("over-cap", st)
	_, st = propose("5.00", otherShop)
	line("other-merchant", st)
	line("done", "yes")
}
