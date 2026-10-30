package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

const ticketUsage = `usage:
  conductor ticket new "<title>" [--description "…" | --description-file <f>] [--assignee bot:<b>|team:<t>]
                        [--priority low|normal|high|urgent] [--due YYYY-MM-DD] [--label <l>]… [--ready] [--attach <file>]…
                        [--spend-limit <amount> [--currency USD] [--card <name>] [--merchant https://…]…]
  conductor ticket list [--assignee bot:<b>|team:<t>|none] [--holder <bot>] [--status <s>[,…]] [--priority <p>] [--label <l>] [--search "…"] [--all]
  conductor ticket show <T-n>
  conductor ticket edit <T-n> [--title "…"] [--description "…"] [--priority <p>] [--due YYYY-MM-DD|none] [--label <l>]… [--no-labels]
                        [--spend-limit <amount>|none [--currency …] [--card …] [--merchant …]…]   # only you set or raise it (a widening)
  conductor ticket assign <T-n> <bot:<b>|team:<t>|none>
  conductor ticket comment <T-n> "<text>" [--attach <file>]…
  conductor ticket ready|wait|done|decline|draft|reopen <T-n> ["<note>"]
      # draft → ready → in_progress → waiting → done | declined. Nothing starts on a draft;
      # ready (with an assignee) hands it to the assignee. Add --as <operator> when several are configured.`

// ticketStatusVerbs map the CLI's verbs to the statuses they set.
var ticketStatusVerbs = map[string]string{"ready": "ready", "wait": "waiting", "resume": "in_progress", "done": "done",
	"decline": "declined", "draft": "draft", "reopen": "reopen"}

// ticketRequest turns `ticket <sub> …` into a ticket op and its arguments
// (shared by the operator's `conductor ticket` and a session's `conductor
// bot ticket`).
func ticketRequest(sub string, pos []string, fl map[string]string) (string, map[string]any, error) {
	a := map[string]any{}
	need := func(n int) error {
		if len(pos) < n {
			return fmt.Errorf("%s", ticketUsage)
		}
		return nil
	}
	labels := func() []string {
		if fl["label"] == "" {
			return nil
		}
		return strings.Split(fl["label"], "\x00")
	}
	desc := func() (string, bool, error) {
		if f := fl["description-file"]; f != "" {
			b, err := os.ReadFile(f)
			return string(b), true, err
		}
		v, ok := fl["description"]
		return v, ok, nil
	}
	switch sub {
	case "new":
		if err := need(1); err != nil {
			return "", nil, err
		}
		a["title"] = strings.Join(pos, " ")
		d, _, err := desc()
		if err != nil {
			return "", nil, err
		}
		a["description"], a["assignee"], a["priority"], a["due"] = d, fl["assignee"], fl["priority"], fl["due"]
		a["labels"], a["ready"] = labels(), fl["ready"] == "true"
		spendFlags(a, fl)
		if err := attachFiles(a, fl["attach"]); err != nil {
			return "", nil, err
		}
		return "ticket_new", a, nil
	case "list", "ls":
		for _, k := range []string{"assignee", "holder", "priority", "label"} {
			a[k] = fl[k]
		}
		a["q"] = fl["search"]
		switch {
		case fl["status"] != "":
			a["status"] = strings.Split(fl["status"], ",")
		case fl["all"] == "true":
			a["status"] = []string{"draft", "ready", "in_progress", "waiting", "done", "declined"}
		}
		return "ticket_list", a, nil
	case "show":
		if err := need(1); err != nil {
			return "", nil, err
		}
		a["ticket"] = pos[0]
		return "ticket_show", a, nil
	case "edit":
		if err := need(1); err != nil {
			return "", nil, err
		}
		a["ticket"] = pos[0]
		for _, k := range []string{"title", "priority", "due"} {
			if v, ok := fl[k]; ok {
				a[k] = v
			}
		}
		if d, ok, err := desc(); err != nil {
			return "", nil, err
		} else if ok {
			a["description"] = d
		}
		if ls := labels(); ls != nil {
			a["labels"] = ls
		} else if fl["no-labels"] == "true" {
			a["labels"] = []string{}
		}
		spendFlags(a, fl)
		return "ticket_edit", a, nil
	case "assign":
		if err := need(2); err != nil {
			return "", nil, err
		}
		a["ticket"], a["to"] = pos[0], pos[1]
		return "ticket_assign", a, nil
	case "comment":
		if err := need(1); err != nil {
			return "", nil, err
		}
		a["ticket"], a["text"] = pos[0], strings.Join(pos[1:], " ")
		if err := attachFiles(a, fl["attach"]); err != nil {
			return "", nil, err
		}
		return "ticket_comment", a, nil
	}
	if to, ok := ticketStatusVerbs[sub]; ok {
		if err := need(1); err != nil {
			return "", nil, err
		}
		a["ticket"], a["to"], a["note"] = pos[0], to, strings.Join(pos[1:], " ")
		return "ticket_status", a, nil
	}
	return "", nil, fmt.Errorf("unknown ticket command %q\n%s", sub, ticketUsage)
}

// spendFlags carries --spend-limit (with --currency, --card, --merchant…).
func spendFlags(a map[string]any, fl map[string]string) {
	v, ok := fl["spend-limit"]
	if !ok {
		return
	}
	a["spend_limit"], a["currency"], a["card"] = v, fl["currency"], fl["card"]
	if fl["merchant"] != "" {
		a["merchants"] = strings.Split(fl["merchant"], "\x00")
	}
}

// cmdTicket is `conductor ticket …`: the operator's tickets.
func cmdTicket(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Println(ticketUsage)
		return nil
	}
	if os.Getenv("CONDUCTOR_SKILL_TOKEN") != "" {
		return fmt.Errorf("refusing: this shell is a conductor agent session — a bot uses `conductor bot ticket …`")
	}
	cfg, rest, err := loadConfig(args[1:])
	if err != nil {
		return err
	}
	pos, fl, _, _ := botFlags(rest)
	op, a, err := ticketRequest(args[0], pos, fl)
	if err != nil {
		return err
	}
	a["as"] = fl["as"]
	out, _, err := botControl(cfg, op, a)
	if err != nil {
		return err
	}
	if fl["json"] == "true" {
		return printJSON(out)
	}
	if op == "ticket_list" {
		fmt.Print(ticketListText(out))
		return nil
	}
	fmt.Print(ticketText(out))
	return nil
}

// ticketListText is one line a ticket.
func ticketListText(out map[string]any) string {
	ts, _ := out["tickets"].([]any)
	if len(ts) == 0 {
		return "no tickets\n"
	}
	var b strings.Builder
	for _, x := range ts {
		t, _ := x.(map[string]any)
		where := fmt.Sprint(t["assignee"])
		if where == "" || where == "<nil>" {
			where = "unassigned"
		}
		if h, _ := t["holder"].(string); h != "" && where != "bot:"+h {
			where += " (bot:" + h + ")"
		}
		fmt.Fprintf(&b, "%-7v %-11v %-7v %-28s %s\n", t["ticket"], t["status"], t["priority"], where, t["title"])
	}
	return b.String()
}

// ticketText is a ticket in full.
func ticketText(t map[string]any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%v  %v\n", t["ticket"], t["title"])
	fmt.Fprintf(&b, "  status:    %v   priority: %v", t["status"], t["priority"])
	if d, ok := t["due"].(string); ok && d != "" {
		if tm, err := time.Parse(time.RFC3339, d); err == nil {
			d = tm.UTC().Format("2006-01-02")
		}
		fmt.Fprintf(&b, "   due: %s", d)
	}
	fmt.Fprintln(&b)
	as := fmt.Sprint(t["assignee"])
	if as == "" || as == "<nil>" {
		as = "unassigned (triage)"
	}
	fmt.Fprintf(&b, "  assignee:  %s", as)
	if h, _ := t["holder"].(string); h != "" {
		fmt.Fprintf(&b, "   held by: bot:%s (case %v)", h, t["id"])
	} else if q, _ := t["queue"].(string); q != "" {
		fmt.Fprintf(&b, "   queue: %s", q)
	}
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "  requester: %v (%v)   created by: %v", t["requester"], t["requester_class"], t["created_by"])
	if r, _ := t["ready_by"].(string); r != "" {
		fmt.Fprintf(&b, "   ready by: %s", r)
	}
	fmt.Fprintln(&b)
	if l, ok := t["spend_limit"].(map[string]any); ok {
		fmt.Fprintf(&b, "  spend limit: %v %v", l["amount"], l["currency"])
		if c, _ := l["card"].(string); c != "" {
			fmt.Fprintf(&b, " on card %s", c)
		}
		if ms, ok := l["merchants"].([]any); ok && len(ms) > 0 {
			fmt.Fprintf(&b, " at %v", ms)
		}
		if sp, ok := t["spent"].(map[string]any); ok {
			if sp["known"] == true {
				fmt.Fprintf(&b, "   spent so far: %v (%v left)", sp["spent"], sp["remaining"])
			} else {
				fmt.Fprintf(&b, "   spent so far: unknown (%v)", sp["why"])
			}
		}
		fmt.Fprintln(&b)
		if h, _ := t["spend_held"].(string); h != "" {
			fmt.Fprintf(&b, "  spend held: %s\n", h)
		}
	}
	if ls, ok := t["labels"].([]any); ok && len(ls) > 0 {
		var l []string
		for _, x := range ls {
			l = append(l, fmt.Sprint(x))
		}
		fmt.Fprintf(&b, "  labels:    %s\n", strings.Join(l, ", "))
	}
	if d, _ := t["description"].(string); strings.TrimSpace(d) != "" {
		fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(d))
	}
	if o, _ := t["outcome"].(string); o != "" {
		fmt.Fprintf(&b, "\n  outcome: %s\n", o)
	}
	if links, ok := t["links"].(map[string]any); ok && len(links) > 0 {
		fmt.Fprintln(&b, "\nlinks:")
		var keys []string
		for k := range links {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %-15s %v\n", k+":", links[k])
		}
	}
	if cs, ok := t["comments"].([]any); ok && len(cs) > 0 {
		fmt.Fprintln(&b, "\ncomments:")
		for _, x := range cs {
			c, _ := x.(map[string]any)
			fmt.Fprintf(&b, "  %s %v: %v\n", shortTime(c["at"]), c["by"], c["text"])
		}
	}
	if hs, ok := t["history"].([]any); ok && len(hs) > 0 {
		fmt.Fprintln(&b, "\nhistory:")
		for _, x := range hs {
			h, _ := x.(map[string]any)
			fmt.Fprintf(&b, "  %s %v: %v\n", shortTime(h["at"]), h["by"], h["what"])
		}
	}
	return b.String()
}

func shortTime(v any) string {
	if s, ok := v.(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UTC().Format("01-02 15:04")
		}
	}
	return fmt.Sprint(v)
}
