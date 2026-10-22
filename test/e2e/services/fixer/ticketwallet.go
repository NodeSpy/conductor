package fixer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// TicketWalletProbeMarker in a worker's brief makes the fake agent propose
// browser purchases as the bot, on the case it was spawned for — so a ticket's
// spend_limit on that case is the binding layer. It records each verdict in
// TICKET_PROBE.md. This is the combined bots + tickets + browser/wallet path
// (group TB): the worker is a real sandbox-launched agent, so its session
// reaches the browser and its purchases carry the bot and its case.
const TicketWalletProbeMarker = "[[ticket-wallet-probe]]"

// TicketWalletProbe runs the probe in dir.
func TicketWalletProbe(dir, prompt string) {
	if !strings.Contains(prompt, TicketWalletProbeMarker) {
		return
	}
	var b strings.Builder
	line := func(k, v string) {
		fmt.Fprintf(&b, "%s: %s\n", k, strings.ReplaceAll(v, "\n", " "))
		_ = os.WriteFile(filepath.Join(dir, "TICKET_PROBE.md"), []byte(b.String()), 0o644)
	}
	buy := func(total string) string {
		out, err := exec.Command("conductor", "browser", "purchase", "--card", "e2ecard",
			"--merchant", "http://localhost:8091", "--total", total, "--currency", "USD",
			"--items", `[{"name":"Widget","qty":1,"amount":`+total+`}]`, "--why", "e2e ticket budget").CombinedOutput()
		return verdict(out, err)
	}
	// Over the ticket's cap: refused by the ticket layer, at propose. Within it:
	// recorded (a commit_money proposal). Over the lifetime total after the
	// first: refused — the cap does not reset per purchase (H2).
	line("over", buy("25.00"))
	line("within", buy("15.00"))
	line("lifetime", buy("10.00"))
	line("done", "yes")
}
