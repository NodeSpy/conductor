package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/bot"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
)

// `conductor bot …` has two faces:
//
//   - from a BOT SESSION (CONDUCTOR_SKILL_TOKEN in env): the agent face,
//     over the agent socket, bound by the token to that one bot and role;
//   - from the OPERATOR's shell: the operator face, over the control socket.
//
// approve/deny are operator-only and SIGNED: the CLI signs the proposal's
// digest with the operator's SSH key (ssh-keygen -Y sign; an ssh-agent key
// works, and a confirming agent asks the human) and the daemon verifies it
// against operators.<name>.ssh_key.

const botUsage = `usage:
  operator:
    conductor bot ls [--folder <path>] [--flat]   # a tree: folder, team, bot
    conductor bot show <bot>
    conductor bot cases <bot> [--all]
    conductor bot workers <bot>
    conductor bot delegations <bot> [--all]
    conductor bot proposals <bot> [--all]
    conductor bot proposal <bot> <id>
    conductor bot approve <bot> <id> [--as <operator>] [--key <path>] [--reason "…"]
    conductor bot deny <bot> <id> [--as <operator>] [--key <path>] [--reason "…"]
    conductor bot rotate <bot>
    conductor bot say <bot> <text…>
    conductor bot deliver <bot> --title "…" [--text "…"] [--requester who] [--urgent]
    conductor bot consolidate <bot>
    conductor bot replan <bot> <id>          # re-plan a stale terraform proposal
    conductor bot sweep <bot>                # run a proactive sweep now (budget still applies)
    conductor bot sweeps <bot>
    conductor bot teams
    conductor bot team <team>                # bots, routing, routed cases, knowledge, pool
    conductor bot knowledge put|rm|mv|revert|ls|cat|log <team> …   # edit a team's knowledge directly (as an operator)
    conductor bot memory put|rm|mv|revert|ls|cat|log <bot> …       # edit a bot's memory directly (as an operator)
    conductor bot approve <bot> <id> --edit-file <f>              # approve a knowledge promotion with your changes
  from a bot session:
    conductor bot whoami
    conductor bot remember --path knowledge/<f>.md --text "…" [--append]
    conductor bot episode [--case <id>] --text "…"
    conductor bot recall <words…>
    conductor bot read <path>
    conductor bot cases [--all]
    conductor bot case open --title "…" [--summary "…"]
    conductor bot case note <case> --text "…"
    conductor bot case close <case> --outcome "…"
    conductor bot spawn [--case <id>] [--title "…"] --brief "…" [--group <label>] [--verbs a,b] [--read-only] [--handoff]
    conductor bot tell <worker|group> "<instructions>"     # to your own workers (home session)
    conductor bot report "<progress>"                      # to your parent (a worker)
    conductor bot instructions [--wait 5m]                 # new instructions from your parent (a worker); --wait blocks for them
    conductor bot join (<group> | --case <id> --group <label>) [--timeout 30m]
    conductor bot ask <bot> [--case <id>] "<text>"        # a linked peer bot (data only)
    conductor bot delegate <bot> [--case <id>] "<task>"   # hand a peer WORK; its outcome comes back to your case
    conductor bot decline <case> "<reason>"                # refuse a delegated case; the reason goes back
    conductor bot delegations [--all]                      # what you delegated (out) and hold (in)
    conductor bot peers
    conductor bot team                                     # your team: teammates, lead, knowledge
    conductor bot team recall <words…> | team read <path>  # the team's shared knowledge
    conductor bot team promote --path knowledge/<f>.md [--to <team path>] [--note "…"]
    conductor bot assign <case> <bot> [--note "…"]         # the team's lead hands a triage case to a teammate
    conductor bot handoff --text "…"
    conductor bot propose-terraform --repo <name> --commit <full sha> [--dir <root module>] [--workspace <ws>] [--var-file <f>]… [--target <addr>]… [--summary "…"]
    conductor bot replan <proposal>
    conductor bot proposals [--all]
    conductor bot wait <proposal> [--timeout 30m]`

var botBoolFlags = map[string]bool{"all": true, "append": true, "handoff": true, "urgent": true, "read-only": true, "flat": true}

// botFlags parses --k v / --k=v, boolean flags, repeated --file, and a
// `--` that ends flag parsing (everything after is positional).
func botFlags(args []string) (pos []string, flags map[string]string, files []string, rest []string) {
	flags = map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "--") {
			pos = append(pos, a)
			continue
		}
		k := strings.TrimPrefix(a, "--")
		v := ""
		if eq := strings.IndexByte(k, '='); eq >= 0 {
			k, v = k[:eq], k[eq+1:]
		} else if botBoolFlags[k] {
			v = "true"
		} else if i+1 < len(args) {
			i++
			v = args[i]
		}
		if k == "file" {
			files = append(files, v)
			continue
		}
		flags[k] = v
	}
	return pos, flags, files, rest
}

var operatorOnly = map[string]bool{"ls": true, "list": true, "show": true, "workers": true, "proposal": true,
	"approve": true, "deny": true, "rotate": true, "say": true, "deliver": true, "consolidate": true,
	"sweep": true, "sweeps": true, "teams": true, "knowledge": true, "memory": true}

func cmdBot(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Println(botUsage)
		return nil
	}
	sub := args[0]
	inSession := os.Getenv("CONDUCTOR_SKILL_TOKEN") != ""
	if operatorOnly[sub] {
		if inSession && (sub == "knowledge" || sub == "memory") {
			return fmt.Errorf("refusing: this shell is a conductor agent session — only an operator edits a repo directly; a bot proposes team knowledge (`conductor bot team promote`) and writes its own memory (`conductor bot remember`)")
		}
		if inSession && (sub == "approve" || sub == "deny") {
			// Not the boundary (that is the signature + the operator socket);
			// a clear refusal for the honest case of an agent trying.
			return fmt.Errorf("refusing: this shell is a conductor agent session — only an operator approves, from their own shell with their own key")
		}
		return botOperator(sub, args[1:])
	}
	if inSession {
		return botAgent(sub, args[1:])
	}
	return botOperator(sub, args[1:])
}

// ---- agent face ----

func botAgentCall(sub string, a map[string]any) (map[string]any, error) {
	resp, err := skillCall(memory.IPCRequest{Op: "bot", Uses: sub, Options: a})
	if err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func botAgent(sub string, args []string) error {
	pos, fl, _, _ := botFlags(args)
	a := map[string]any{}
	for k, v := range fl {
		a[k] = v
	}
	call := sub
	switch sub {
	case "whoami", "remember", "episode", "handoff", "proposals", "cases", "peers", "delegations":
	case "instructions":
		// --wait blocks until the parent sends something (or the timeout):
		// how a child waits on its parent, like wait/join.
		if fl["wait"] == "" {
			break
		}
		d, err := time.ParseDuration(fl["wait"])
		if err != nil {
			return err
		}
		deadline := time.Now().Add(d)
		for {
			out, err := botAgentCall("instructions", map[string]any{})
			if err != nil {
				return err
			}
			if ins, ok := out["instructions_from_parent"].(string); ok {
				fmt.Println(ins)
				return nil
			}
			if time.Now().After(deadline) {
				fmt.Println("no new instructions from your parent")
				return nil
			}
			time.Sleep(2 * time.Second)
		}
	case "spawn":
		if fl["read-only"] != "" {
			a["read_only"] = true
			delete(a, "read-only")
		}
	case "tell":
		if len(pos) < 2 {
			return fmt.Errorf("usage: conductor bot tell <worker|group> \"<instructions>\"")
		}
		a["to"], a["text"] = pos[0], strings.Join(pos[1:], " ")
	case "report":
		if t := strings.Join(pos, " "); t != "" {
			a["text"] = t
		}
	case "ask":
		if len(pos) < 2 {
			return fmt.Errorf("usage: conductor bot ask <bot> [--case <id>] \"<text>\"")
		}
		a["to"], a["text"] = pos[0], strings.Join(pos[1:], " ")
	case "delegate":
		if len(pos) < 2 {
			return fmt.Errorf("usage: conductor bot delegate <bot> [--case <id>] \"<task>\"")
		}
		a["to"], a["task"] = pos[0], strings.Join(pos[1:], " ")
	case "decline":
		if len(pos) < 2 {
			return fmt.Errorf("usage: conductor bot decline <case> \"<reason>\"")
		}
		a["case"], a["reason"] = pos[0], strings.Join(pos[1:], " ")
	case "join":
		if len(pos) == 1 {
			a["id"] = pos[0]
		}
		to := 30 * time.Minute
		if s := fl["timeout"]; s != "" {
			d, err := time.ParseDuration(s)
			if err != nil {
				return err
			}
			to = d
		}
		delete(a, "timeout")
		deadline := time.Now().Add(to)
		for {
			out, err := botAgentCall("join", a)
			if err != nil {
				return err
			}
			if done, _ := out["done"].(bool); done || time.Now().After(deadline) {
				return printJSON(out)
			}
		}
	case "team":
		if len(pos) > 0 {
			a["action"] = pos[0]
			switch pos[0] {
			case "recall":
				a["query"] = strings.Join(pos[1:], " ")
			case "read":
				if len(pos) != 2 {
					return fmt.Errorf("usage: conductor bot team read <path>")
				}
				a["path"] = pos[1]
			}
		}
	case "assign":
		if len(pos) != 2 {
			return fmt.Errorf("usage: conductor bot assign <case> <bot> [--note \"…\"]")
		}
		a["case"], a["to"] = pos[0], pos[1]
	case "recall":
		a["query"] = strings.Join(pos, " ")
	case "read":
		if len(pos) != 1 {
			return fmt.Errorf("usage: conductor bot read <path>")
		}
		a["path"] = pos[0]
	case "case":
		if len(pos) == 0 {
			return fmt.Errorf("usage: conductor bot case open|note|close …")
		}
		switch pos[0] {
		case "open":
			call = "case_open"
		case "note", "close":
			if len(pos) < 2 {
				return fmt.Errorf("usage: conductor bot case %s <case> …", pos[0])
			}
			call = "case_" + pos[0]
			a["case"] = pos[1]
		default:
			return fmt.Errorf("unknown case action %q", pos[0])
		}
	case "propose-terraform":
		call = "propose_terraform"
		// Repeatable flags: --var-file / --target.
		var vfs, tgts []any
		for k := 0; k < len(args); k++ {
			if (args[k] == "--var-file" || args[k] == "--target") && k+1 < len(args) {
				if args[k] == "--var-file" {
					vfs = append(vfs, args[k+1])
				} else {
					tgts = append(tgts, args[k+1])
				}
				k++
			}
		}
		delete(a, "var-file")
		delete(a, "target")
		a["var_files"], a["targets"] = vfs, tgts
	case "replan":
		if len(pos) != 1 {
			return fmt.Errorf("usage: conductor bot replan <proposal>")
		}
		a["proposal"] = pos[0]
	case "wait":
		if len(pos) != 1 {
			return fmt.Errorf("usage: conductor bot wait <proposal> [--timeout 30m]")
		}
		to := 30 * time.Minute
		if s := fl["timeout"]; s != "" {
			d, err := time.ParseDuration(s)
			if err != nil {
				return err
			}
			to = d
		}
		deadline := time.Now().Add(to)
		for {
			out, err := botAgentCall("wait", map[string]any{"proposal": pos[0]})
			if err != nil {
				return err
			}
			showInstructions(out)
			if st, _ := out["status"].(string); !bot.Unsettled(st) || time.Now().After(deadline) {
				return printJSON(out)
			}
		}
	default:
		return fmt.Errorf("unknown bot command %q\n%s", sub, botUsage)
	}
	out, err := botAgentCall(call, a)
	if err != nil {
		return err
	}
	showInstructions(out)
	if msg, ok := out["message"].(string); ok && len(out) <= 7 {
		fmt.Println(msg)
	}
	return printJSON(out)
}

// showInstructions prints instructions from a worker's parent first and
// plainly, so the agent cannot miss them inside the JSON.
func showInstructions(out map[string]any) {
	if ins, ok := out["instructions_from_parent"].(string); ok {
		fmt.Println(ins)
		delete(out, "instructions_from_parent")
	}
}

// ---- operator face ----

func botControl(cfg *config.Config, sub string, a map[string]any) (map[string]any, string, error) {
	p := controlSockPath(cfg)
	conn, err := net.Dial("unix", p)
	if err != nil {
		return nil, "", fmt.Errorf("connect to daemon control socket %s — is it running? %w", p, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Minute))
	if err := json.NewEncoder(conn).Encode(controlRequest{Cmd: "bot", Sub: sub, Args: a}); err != nil {
		return nil, "", err
	}
	var resp controlResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, "", err
	}
	if resp.Error != "" {
		return nil, "", fmt.Errorf("%s", resp.Error)
	}
	return resp.Result, resp.Msg, nil
}

func botOperator(sub string, args []string) error {
	for i, a := range args {
		if a == "-m" {
			args[i] = "--message"
		}
	}
	cfg, rest, err := loadConfig(args)
	if err != nil {
		return err
	}
	pos, fl, _, _ := botFlags(rest)
	a := map[string]any{}
	for k, v := range fl {
		a[k] = v
	}
	needBot := func(n int) error {
		if len(pos) < n {
			return fmt.Errorf("usage:\n%s", botUsage)
		}
		a["bot"] = pos[0]
		return nil
	}
	switch sub {
	case "knowledge", "memory":
		return botRepoEdit(cfg, sub, pos, fl)
	case "ls", "list":
		folder, err := config.NormFolder(fl["folder"])
		if err != nil {
			return fmt.Errorf("--folder: %w", err)
		}
		out, _, err := botControl(cfg, "list", map[string]any{})
		if err != nil {
			return err
		}
		fmt.Print(botListText(out, folder, fl["flat"] == "true"))
		return nil
	case "teams":
	case "team":
		if len(pos) != 1 {
			return fmt.Errorf("usage: conductor bot team <team>")
		}
		a["team"] = pos[0]
	case "show", "cases", "workers", "delegations", "proposals", "rotate", "consolidate", "sweep", "sweeps":
		if err := needBot(1); err != nil {
			return err
		}
	case "replan":
		if err := needBot(2); err != nil {
			return err
		}
		a["id"] = pos[1]
	case "proposal":
		if err := needBot(2); err != nil {
			return err
		}
		a["id"] = pos[1]
	case "say":
		if err := needBot(2); err != nil {
			return err
		}
		a["text"] = strings.Join(pos[1:], " ")
	case "deliver":
		if err := needBot(1); err != nil {
			return err
		}
	case "approve", "deny":
		if err := needBot(2); err != nil {
			return err
		}
		return botDecide(cfg, sub, pos[0], pos[1], fl)
	default:
		return fmt.Errorf("unknown bot command %q\n%s", sub, botUsage)
	}
	out, msg, err := botControl(cfg, sub, a)
	if err != nil {
		return err
	}
	if msg != "" {
		fmt.Println(msg)
	}
	if out == nil {
		return nil
	}
	return printJSON(out)
}

const botRepoUsage = `usage:
  conductor bot %[1]s put <%[2]s> <path> (--from-file <f> | --text "…") --base <commit> [-m "…"] [--as <operator>]%[3]s
  conductor bot %[1]s rm <%[2]s> <path> --base <commit> [-m "…"] [--as <operator>]%[3]s
  conductor bot %[1]s mv <%[2]s> <path> <new path> --base <commit> [-m "…"] [--as <operator>]%[3]s
  conductor bot %[1]s revert <%[2]s> <commit> --base <commit> [-m "…"] [--as <operator>]%[3]s
  conductor bot %[1]s ls <%[2]s>               # files, and the head to write against
  conductor bot %[1]s cat <%[2]s> <path>       # a file (the head on stderr)
  conductor bot %[1]s log <%[2]s> [<path>] [--limit N]   # commits: author, kind, case, proposal`

// botRepoEdit is `conductor bot knowledge|memory …`: an operator's direct
// reads and writes of a team's knowledge repo or a bot's memory repo.
func botRepoEdit(cfg *config.Config, kind string, pos []string, fl map[string]string) error {
	who, extra := "team", ""
	if kind == "memory" {
		who, extra = "bot", " [--case <id>]"
	}
	usage := fmt.Errorf(botRepoUsage, kind, who, extra)
	if len(pos) < 2 {
		return usage
	}
	action, target := pos[0], pos[1]
	a := map[string]any{who: target}
	need := func(n int) error {
		if len(pos) != n {
			return usage
		}
		return nil
	}
	switch action {
	case "ls", "cat":
		if action == "cat" {
			if err := need(3); err != nil {
				return err
			}
			a["path"] = pos[2]
		} else if err := need(2); err != nil {
			return err
		}
		out, _, err := botControl(cfg, "file", a)
		if err != nil {
			return err
		}
		if action == "cat" {
			fmt.Fprintf(os.Stderr, "%s %v at %v\n", out["repo"], out["path"], out["head"])
			fmt.Print(out["text"])
			return nil
		}
		return printJSON(out)
	case "log":
		if len(pos) > 3 {
			return usage
		}
		if len(pos) == 3 {
			a["path"] = pos[2]
		}
		a["limit"] = fl["limit"]
		out, _, err := botControl(cfg, "history", a)
		if err != nil {
			return err
		}
		cs, _ := out["commits"].([]any)
		fmt.Printf("%v — head %v\n", out["repo"], out["head"])
		for _, x := range cs {
			c, _ := x.(map[string]any)
			link := ""
			for _, k := range []string{"case", "proposal", "bot", "reverts"} {
				if v, _ := c[k].(string); v != "" {
					link += fmt.Sprintf(" %s=%s", k, v)
				}
			}
			fmt.Printf("%v %-25.25v %-8v %-14v %v%s\n", c["short"], c["date"], c["kind"], c["who"], c["subject"], link)
		}
		return nil
	case "put", "rm", "mv", "revert":
	default:
		return usage
	}
	a["op"], a["base"], a["message"], a["as"], a["case"] = action, fl["base"], fl["message"], fl["as"], fl["case"]
	if fl["base"] == "" {
		return fmt.Errorf("--base <commit> is required: the commit you read (`conductor bot %s ls %s` shows the head) — a write against a stale base is refused", kind, target)
	}
	switch action {
	case "put":
		if err := need(3); err != nil {
			return err
		}
		a["path"] = pos[2]
		switch {
		case fl["from-file"] != "" && fl["text"] != "":
			return fmt.Errorf("--from-file or --text, not both")
		case fl["from-file"] != "":
			b, err := os.ReadFile(fl["from-file"])
			if err != nil {
				return err
			}
			a["text"] = string(b)
		case fl["text"] != "":
			a["text"] = fl["text"]
		default:
			return fmt.Errorf("put needs the content: --from-file <f> or --text \"…\"")
		}
	case "rm":
		if err := need(3); err != nil {
			return err
		}
		a["path"] = pos[2]
	case "mv":
		if err := need(4); err != nil {
			return err
		}
		a["path"], a["to"] = pos[2], pos[3]
	case "revert":
		if err := need(3); err != nil {
			return err
		}
		a["commit"] = pos[2]
	}
	out, _, err := botControl(cfg, "edit", a)
	if err != nil {
		return err
	}
	if out["changed"] == false {
		fmt.Printf("%v: unchanged (head %v)\n", out["repo"], out["commit"])
		return nil
	}
	fmt.Printf("%v: %s committed %v as operator %v\n", out["repo"], action, out["commit"], out["operator"])
	return nil
}

// botListText renders `conductor bot ls`: a tree grouped by folder, then
// team, then bot — or, with flat, one line per bot. A folder filter keeps
// that folder and everything under it.
func botListText(out map[string]any, folder string, flat bool) string {
	ms, _ := out["bots"].([]any)
	type row struct{ folder, team, bot, line string }
	var rows []row
	for _, x := range ms {
		m, _ := x.(map[string]any)
		f, _ := m["folder"].(string)
		if folder != "" && f != folder && !strings.HasPrefix(f, folder+"/") {
			continue
		}
		t, _ := m["team"].(string)
		rows = append(rows, row{f, t, fmt.Sprint(m["bot"]), botLine(m)})
	}
	if len(rows) == 0 {
		if folder != "" {
			return fmt.Sprintf("no bots in folder %s\n", folder)
		}
		return "no bots\n"
	}
	// Folder, then team (teamless last), then bot.
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.folder != b.folder {
			return a.folder < b.folder
		}
		if (a.team == "") != (b.team == "") {
			return a.team != ""
		}
		if a.team != b.team {
			return a.team < b.team
		}
		return a.bot < b.bot
	})
	var sb strings.Builder
	if flat {
		for _, r := range rows {
			fmt.Fprintf(&sb, "%-16s %-20s %-14s %s\n", r.bot, orRoot(r.folder), orDashCLI(r.team), r.line)
		}
		return sb.String()
	}
	var printed []string // the folder segments already printed
	lastTeam, lastFolder := "\x00", "\x00"
	for _, r := range rows {
		var segs []string
		if r.folder != "" {
			segs = strings.Split(r.folder, "/")
		}
		if r.folder != lastFolder {
			common := 0
			for common < len(printed) && common < len(segs) && printed[common] == segs[common] {
				common++
			}
			for d := common; d < len(segs); d++ {
				fmt.Fprintf(&sb, "%s%s/\n", strings.Repeat("  ", d), segs[d])
			}
			printed, lastFolder, lastTeam = segs, r.folder, "\x00"
		}
		ind := strings.Repeat("  ", len(segs))
		if r.team != lastTeam {
			if r.team != "" {
				fmt.Fprintf(&sb, "%steam %s\n", ind, r.team)
			}
			lastTeam = r.team
		}
		if r.team != "" {
			ind += "  "
		}
		fmt.Fprintf(&sb, "%s%-16s %s\n", ind, r.bot, r.line)
	}
	return sb.String()
}

func botLine(m map[string]any) string {
	home := "no home session"
	if h, ok := m["home"].(map[string]any); ok {
		home = fmt.Sprintf("home %v (agent %v, %v turns)", h["session"], h["agent"], h["turns"])
	}
	mode := "live"
	if m["shadow"] == true {
		mode = "shadow"
	}
	return fmt.Sprintf("%-6s %s · %v open cases · %v pending proposals · %v workers", mode, home, m["open_cases"], m["pending_proposals"], m["live_workers"])
}

func orRoot(f string) string {
	if f == "" {
		return "/"
	}
	return f
}

func orDashCLI(t string) string {
	if t == "" {
		return "-"
	}
	return t
}

// botDecide signs and submits an approval or denial.
func botDecide(cfg *config.Config, decision, name, id string, fl map[string]string) error {
	p, _, err := botControl(cfg, "proposal", map[string]any{"bot": name, "id": id})
	if err != nil {
		return err
	}
	ops := []string{}
	if xs, ok := p["operators"].([]any); ok {
		for _, x := range xs {
			ops = append(ops, fmt.Sprint(x))
		}
	}
	op := fl["as"]
	if op == "" {
		if len(ops) != 1 {
			sort.Strings(ops)
			return fmt.Errorf("say which operator you are with --as (operators of %s: %s)", name, strings.Join(ops, ", "))
		}
		op = ops[0]
	}
	opCfg, ok := cfg.Operators[op]
	if !ok {
		return fmt.Errorf("operator %q is not configured", op)
	}
	digest, _ := p["digest"].(string)
	// Approve with changes: --edit-file holds the final content (a
	// knowledge promotion). The signature covers its hash.
	edit := ""
	if f := fl["edit-file"]; f != "" {
		if decision != "approve" {
			return fmt.Errorf("--edit-file goes with approve")
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if len(b) == 0 {
			return fmt.Errorf("--edit-file %s is empty", f)
		}
		edit = string(b)
		fmt.Printf("with changes: %d bytes from %s replace the proposed text\n", len(b), f)
	}
	dash := func(v any) any {
		if v == nil || v == "" {
			return "-"
		}
		return v
	}
	fmt.Printf("%s proposal %s (bot %s, case %v, tier %v):\n  %v\n  requested by %v (%v); status %v\n",
		map[string]string{"approve": "Approving", "deny": "Denying"}[decision], id, name, dash(p["case_id"]), p["tier"], p["action"], dash(p["requester"]), p["requester_class"], p["status"])
	key := fl["key"]
	if key == "" {
		// Sign through the ssh-agent with the configured public key.
		f, err := os.CreateTemp("", "conductor-operator-*.pub")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		if _, err := f.WriteString(strings.TrimSpace(opCfg.SSHKey) + "\n"); err != nil {
			return err
		}
		f.Close()
		key = f.Name()
	}
	sig, err := bot.SignApproval(context.Background(), key, bot.ApprovalPayload(name, id, digest, decision, edit, fl["reason"]))
	if err != nil {
		return fmt.Errorf("%w\n(no ssh-agent holding operators.%s.ssh_key? pass --key <private key file>)", err, op)
	}
	out, _, err := botControl(cfg, "decide", map[string]any{"bot": name, "id": id, "operator": op,
		"decision": decision, "signature": sig, "reason": fl["reason"], "edit": edit})
	if err != nil {
		return err
	}
	fmt.Printf("proposal %s: %v\n", id, out["status"])
	return printJSON(out)
}
