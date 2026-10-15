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

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/member"
	"github.com/NodeSpy/conductor/internal/memory"
)

// `conductor member …` has two faces:
//
//   - from a MEMBER SESSION (CONDUCTOR_SKILL_TOKEN in env): the agent face,
//     over the agent socket, bound by the token to that one member and role;
//   - from the OPERATOR's shell: the operator face, over the control socket.
//
// approve/deny are operator-only and SIGNED: the CLI signs the proposal's
// digest with the operator's SSH key (ssh-keygen -Y sign; an ssh-agent key
// works, and a confirming agent asks the human) and the daemon verifies it
// against operators.<name>.ssh_key.

const memberUsage = `usage:
  operator:
    conductor member ls
    conductor member show <member>
    conductor member cases <member> [--all]
    conductor member workers <member>
    conductor member proposals <member> [--all]
    conductor member proposal <member> <id>
    conductor member approve <member> <id> [--as <operator>] [--key <path>] [--reason "…"]
    conductor member deny <member> <id> [--as <operator>] [--key <path>] [--reason "…"]
    conductor member rotate <member>
    conductor member say <member> <text…>
    conductor member deliver <member> --title "…" [--text "…"] [--requester who] [--urgent]
    conductor member consolidate <member>
    conductor member replan <member> <id>          # re-plan a stale terraform proposal
  from a member session:
    conductor member whoami
    conductor member remember --path knowledge/<f>.md --text "…" [--append]
    conductor member episode [--case <id>] --text "…"
    conductor member recall <words…>
    conductor member read <path>
    conductor member cases [--all]
    conductor member case open --title "…" [--summary "…"]
    conductor member case note <case> --text "…"
    conductor member case close <case> --outcome "…"
    conductor member spawn [--case <id>] [--title "…"] --brief "…" [--handoff]
    conductor member handoff --text "…"
    conductor member propose-terraform --repo <name> --commit <full sha> [--dir <root module>] [--workspace <ws>] [--var-file <f>]… [--target <addr>]… [--summary "…"]
    conductor member replan <proposal>
    conductor member proposals [--all]
    conductor member wait <proposal> [--timeout 30m]`

var memberBoolFlags = map[string]bool{"all": true, "append": true, "handoff": true, "urgent": true}

// memberFlags parses --k v / --k=v, boolean flags, repeated --file, and a
// `--` that ends flag parsing (everything after is positional).
func memberFlags(args []string) (pos []string, flags map[string]string, files []string, rest []string) {
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
		} else if memberBoolFlags[k] {
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
	"approve": true, "deny": true, "rotate": true, "say": true, "deliver": true, "consolidate": true}

func cmdMember(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Println(memberUsage)
		return nil
	}
	sub := args[0]
	inSession := os.Getenv("CONDUCTOR_SKILL_TOKEN") != ""
	if operatorOnly[sub] {
		if inSession && (sub == "approve" || sub == "deny") {
			// Not the boundary (that is the signature + the operator socket);
			// a clear refusal for the honest case of an agent trying.
			return fmt.Errorf("refusing: this shell is a conductor agent session — only an operator approves, from their own shell with their own key")
		}
		return memberOperator(sub, args[1:])
	}
	if inSession {
		return memberAgent(sub, args[1:])
	}
	return memberOperator(sub, args[1:])
}

// ---- agent face ----

func memberAgentCall(sub string, a map[string]any) (map[string]any, error) {
	resp, err := skillCall(memory.IPCRequest{Op: "member", Uses: sub, Options: a})
	if err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func memberAgent(sub string, args []string) error {
	pos, fl, _, _ := memberFlags(args)
	a := map[string]any{}
	for k, v := range fl {
		a[k] = v
	}
	call := sub
	switch sub {
	case "whoami", "remember", "episode", "spawn", "handoff", "proposals", "cases":
	case "recall":
		a["query"] = strings.Join(pos, " ")
	case "read":
		if len(pos) != 1 {
			return fmt.Errorf("usage: conductor member read <path>")
		}
		a["path"] = pos[0]
	case "case":
		if len(pos) == 0 {
			return fmt.Errorf("usage: conductor member case open|note|close …")
		}
		switch pos[0] {
		case "open":
			call = "case_open"
		case "note", "close":
			if len(pos) < 2 {
				return fmt.Errorf("usage: conductor member case %s <case> …", pos[0])
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
			return fmt.Errorf("usage: conductor member replan <proposal>")
		}
		a["proposal"] = pos[0]
	case "wait":
		if len(pos) != 1 {
			return fmt.Errorf("usage: conductor member wait <proposal> [--timeout 30m]")
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
			out, err := memberAgentCall("wait", map[string]any{"proposal": pos[0]})
			if err != nil {
				return err
			}
			if st, _ := out["status"].(string); !member.Unsettled(st) || time.Now().After(deadline) {
				return printJSON(out)
			}
		}
	default:
		return fmt.Errorf("unknown member command %q\n%s", sub, memberUsage)
	}
	out, err := memberAgentCall(call, a)
	if err != nil {
		return err
	}
	if msg, ok := out["message"].(string); ok && len(out) <= 7 {
		fmt.Println(msg)
	}
	return printJSON(out)
}

// ---- operator face ----

func memberControl(cfg *config.Config, sub string, a map[string]any) (map[string]any, string, error) {
	p := controlSockPath(cfg)
	conn, err := net.Dial("unix", p)
	if err != nil {
		return nil, "", fmt.Errorf("connect to daemon control socket %s — is it running? %w", p, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Minute))
	if err := json.NewEncoder(conn).Encode(controlRequest{Cmd: "member", Sub: sub, Args: a}); err != nil {
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

func memberOperator(sub string, args []string) error {
	cfg, rest, err := loadConfig(args)
	if err != nil {
		return err
	}
	pos, fl, _, _ := memberFlags(rest)
	a := map[string]any{}
	for k, v := range fl {
		a[k] = v
	}
	needMember := func(n int) error {
		if len(pos) < n {
			return fmt.Errorf("usage:\n%s", memberUsage)
		}
		a["member"] = pos[0]
		return nil
	}
	switch sub {
	case "ls", "list":
		out, _, err := memberControl(cfg, "list", a)
		if err != nil {
			return err
		}
		return printMemberList(out)
	case "show", "cases", "workers", "proposals", "rotate", "consolidate":
		if err := needMember(1); err != nil {
			return err
		}
	case "replan":
		if err := needMember(2); err != nil {
			return err
		}
		a["id"] = pos[1]
	case "proposal":
		if err := needMember(2); err != nil {
			return err
		}
		a["id"] = pos[1]
	case "say":
		if err := needMember(2); err != nil {
			return err
		}
		a["text"] = strings.Join(pos[1:], " ")
	case "deliver":
		if err := needMember(1); err != nil {
			return err
		}
	case "approve", "deny":
		if err := needMember(2); err != nil {
			return err
		}
		return memberDecide(cfg, sub, pos[0], pos[1], fl)
	default:
		return fmt.Errorf("unknown member command %q\n%s", sub, memberUsage)
	}
	out, msg, err := memberControl(cfg, sub, a)
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

func printMemberList(out map[string]any) error {
	ms, _ := out["members"].([]any)
	if len(ms) == 0 {
		fmt.Println("no members")
		return nil
	}
	for _, x := range ms {
		m, _ := x.(map[string]any)
		home := "no home session"
		if h, ok := m["home"].(map[string]any); ok {
			home = fmt.Sprintf("home %v (agent %v, %v turns)", h["session"], h["agent"], h["turns"])
		}
		mode := "live"
		if m["shadow"] == true {
			mode = "shadow"
		}
		fmt.Printf("%-16v %-6s %s · %v open cases · %v pending proposals · %v workers\n",
			m["member"], mode, home, m["open_cases"], m["pending_proposals"], m["live_workers"])
	}
	return nil
}

// memberDecide signs and submits an approval or denial.
func memberDecide(cfg *config.Config, decision, name, id string, fl map[string]string) error {
	p, _, err := memberControl(cfg, "proposal", map[string]any{"member": name, "id": id})
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
	dash := func(v any) any {
		if v == nil || v == "" {
			return "-"
		}
		return v
	}
	fmt.Printf("%s proposal %s (member %s, case %v, tier %v):\n  %v\n  requested by %v (%v); status %v\n",
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
	sig, err := member.SignApproval(context.Background(), key, member.ApprovalPayload(name, id, digest, decision))
	if err != nil {
		return fmt.Errorf("%w\n(no ssh-agent holding operators.%s.ssh_key? pass --key <private key file>)", err, op)
	}
	out, _, err := memberControl(cfg, "decide", map[string]any{"member": name, "id": id, "operator": op,
		"decision": decision, "signature": sig, "reason": fl["reason"]})
	if err != nil {
		return err
	}
	fmt.Printf("proposal %s: %v\n", id, out["status"])
	return printJSON(out)
}
