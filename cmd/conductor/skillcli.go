package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/memory"
)

// The agent-facing skill CLI (#36 §12, CLI face). A dispatched agent reaches
// back into conductor by shelling these commands — no MCP server, no config
// injection. The daemon endpoint + one-shot-ish session token arrive in the
// agent's environment (set by the dispatch path):
//
//	CONDUCTOR_ENDPOINT     unix://<memory.sock>   (https://… for a remote agent)
//	CONDUCTOR_SKILL_TOKEN  the uid-bound session token (broker.MintSession)
//
// Every op authorizes by that token plus, on the local socket, the calling
// process's kernel uid (read server-side) — so a token scraped from env is
// useless from another uid, and every call is audited daemon-side.
//
//	conductor discover [connector | connector.verb | -s <query>]
//	conductor call <connector.verb> [--opt value ...]
//	conductor memory recall <query> | remember <text> [--tags a,b] [--scope s]
//	conductor secret <name>

// skillEndpoint resolves the daemon endpoint from the environment. Only the
// unix transport is wired today; https:// is the remote-agent path (a later
// increment) and reports a clear "not yet" rather than silently failing.
func skillEndpoint() (socket string, err error) {
	ep := strings.TrimSpace(os.Getenv("CONDUCTOR_ENDPOINT"))
	if ep == "" {
		return "", fmt.Errorf("CONDUCTOR_ENDPOINT is not set — this command only runs inside a conductor-dispatched agent")
	}
	switch {
	case strings.HasPrefix(ep, "unix://"):
		return strings.TrimPrefix(ep, "unix://"), nil
	case strings.HasPrefix(ep, "https://"), strings.HasPrefix(ep, "http://"):
		return "", fmt.Errorf("remote (%s) skill endpoints are not supported by this build yet", ep)
	default:
		return ep, nil // a bare path
	}
}

// skillCall dials the daemon endpoint for one op, stamping the session token.
func skillCall(req memory.IPCRequest) (memory.IPCResponse, error) {
	socket, err := skillEndpoint()
	if err != nil {
		return memory.IPCResponse{}, err
	}
	req.Token = os.Getenv("CONDUCTOR_SKILL_TOKEN")
	resp, err := memory.IPCCall(socket, req)
	if err != nil {
		return resp, err
	}
	if resp.Error != "" {
		return resp, fmt.Errorf("%s", resp.Error)
	}
	return resp, nil
}

// cmdCall runs one connector verb through conductor: `conductor call gh.comment
// --body "…"`. Conductor executes it server-side with its own credentials — the
// raw secret never enters this session.
func cmdCall(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: conductor call <connector.verb> [--opt value ...]  (see `conductor discover`)")
	}
	uses := args[0]
	opts, err := parseOpts(args[1:])
	if err != nil {
		return err
	}
	resp, err := skillCall(memory.IPCRequest{Op: "verb", Uses: uses, Options: opts})
	if err != nil {
		return err
	}
	return printJSON(resp.Result)
}

// parseOpts turns `--key value` / `--key=value` / `--flag` pairs into an
// options map, JSON-decoding values so numbers/bools/objects pass through typed.
func parseOpts(args []string) (map[string]any, error) {
	opts := map[string]any{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			return nil, fmt.Errorf("unexpected argument %q (options are --key value)", a)
		}
		key := strings.TrimPrefix(a, "--")
		var val string
		if eq := strings.IndexByte(key, '='); eq >= 0 {
			key, val = key[:eq], key[eq+1:]
		} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			i++
			val = args[i]
		} else {
			opts[key] = true // bare flag
			continue
		}
		opts[key] = coerce(val)
	}
	return opts, nil
}

// coerce keeps a value typed when it's valid JSON (number/bool/array/object),
// else treats it as a literal string.
func coerce(s string) any {
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		switch v.(type) {
		case float64, bool, []any, map[string]any:
			return v
		}
	}
	return s
}

// cmdDiscover browses the verbs THIS agent's token may use, grouped by
// connector, searchable, and describable — progressive disclosure so a large
// allowlist never has to be dumped into the prompt.
func cmdDiscover(args []string) error {
	tools, err := fetchVerbTools()
	if err != nil {
		return err
	}
	// `-s <query>` search across all allowed verbs.
	if len(args) >= 1 && (args[0] == "-s" || args[0] == "--search") {
		q := strings.ToLower(strings.Join(args[1:], " "))
		var hits []verbTool
		for _, t := range tools {
			if strings.Contains(strings.ToLower(t.Uses), q) || strings.Contains(strings.ToLower(t.Desc), q) {
				hits = append(hits, t)
			}
		}
		return printVerbList(hits)
	}
	if len(args) == 0 {
		// Top level: the connectors this agent can act through.
		counts := map[string]int{}
		for _, t := range tools {
			counts[connOf(t.Uses)]++
		}
		names := make([]string, 0, len(counts))
		for c := range counts {
			names = append(names, c)
		}
		sort.Strings(names)
		if len(names) == 0 {
			fmt.Println("No verbs are available to this agent.")
			return nil
		}
		fmt.Println("Connectors you can act through (conductor discover <connector> to list verbs):")
		for _, c := range names {
			fmt.Printf("  %-16s %d verb(s)\n", c, counts[c])
		}
		fmt.Println("\nAlso: conductor memory recall|remember · conductor secret <name>")
		return nil
	}
	arg := args[0]
	if strings.Contains(arg, ".") { // a specific verb → full detail
		for _, t := range tools {
			if t.Uses == arg {
				return describeVerb(t)
			}
		}
		return fmt.Errorf("verb %q is not available to this agent (try `conductor discover -s %s`)", arg, arg)
	}
	// A connector name → its verbs.
	var in []verbTool
	for _, t := range tools {
		if connOf(t.Uses) == arg {
			in = append(in, t)
		}
	}
	if len(in) == 0 {
		return fmt.Errorf("no connector %q available (try `conductor discover`)", arg)
	}
	return printVerbList(in)
}

type verbTool struct {
	Uses   string
	Desc   string
	Schema map[string]any
}

func connOf(uses string) string {
	if i := strings.IndexByte(uses, '.'); i >= 0 {
		return uses[:i]
	}
	return uses
}

func fetchVerbTools() ([]verbTool, error) {
	resp, err := skillCall(memory.IPCRequest{Op: "verb_list"})
	if err != nil {
		return nil, err
	}
	raw, _ := resp.Result["tools"].([]any)
	out := make([]verbTool, 0, len(raw))
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		uses, _ := m["uses"].(string)
		desc, _ := m["description"].(string)
		schema, _ := m["inputSchema"].(map[string]any)
		out = append(out, verbTool{Uses: uses, Desc: desc, Schema: schema})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Uses < out[j].Uses })
	return out, nil
}

func printVerbList(tools []verbTool) error {
	if len(tools) == 0 {
		fmt.Println("No matching verbs.")
		return nil
	}
	for _, t := range tools {
		fmt.Printf("  %-24s %s\n", t.Uses, firstLine(t.Desc))
	}
	fmt.Println("\nconductor discover <connector.verb> for options; conductor call <verb> --opt value to run.")
	return nil
}

func describeVerb(t verbTool) error {
	fmt.Printf("%s\n", t.Uses)
	if t.Desc != "" {
		fmt.Printf("  %s\n", t.Desc)
	}
	props, _ := t.Schema["properties"].(map[string]any)
	if len(props) == 0 {
		fmt.Println("  (no options)")
		return nil
	}
	req := map[string]bool{}
	if rs, ok := t.Schema["required"].([]any); ok {
		for _, r := range rs {
			if s, ok := r.(string); ok {
				req[s] = true
			}
		}
	}
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Println("  options:")
	for _, n := range names {
		p, _ := props[n].(map[string]any)
		typ, _ := p["type"].(string)
		desc, _ := p["description"].(string)
		flag := ""
		if req[n] {
			flag = " (required)"
		}
		fmt.Printf("    --%-16s %s%s  %s\n", n, typ, flag, desc)
	}
	fmt.Printf("\nrun: conductor call %s --<option> <value> ...\n", t.Uses)
	return nil
}

// cmdSkillMemory is the agent's memory face: recall (read) and remember (write)
// over the configured [[Memory]], through the same session token.
func cmdSkillMemory(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: conductor memory recall <query> | remember <text> [--tags a,b] [--scope s]")
	}
	switch args[0] {
	case "recall":
		resp, err := skillCall(memory.IPCRequest{Op: "recall", Substring: strings.Join(args[1:], " ")})
		if err != nil {
			return err
		}
		if len(resp.Entries) == 0 {
			fmt.Println("(no memories)")
			return nil
		}
		for _, e := range resp.Entries {
			fmt.Printf("- %s\n", e.Text)
		}
		return nil
	case "remember":
		rest, opts, err := splitFlags(args[1:])
		if err != nil {
			return err
		}
		req := memory.IPCRequest{Op: "remember", Text: strings.Join(rest, " ")}
		if s, ok := opts["scope"]; ok {
			req.Scope = s
		}
		if t, ok := opts["tags"]; ok {
			req.Tags = strings.Split(t, ",")
		}
		if _, err := skillCall(req); err != nil {
			return err
		}
		fmt.Println("remembered")
		return nil
	default:
		return fmt.Errorf("conductor memory: unknown subcommand %q (recall | remember)", args[0])
	}
}

// cmdSecret is the last-resort broker path: issue a single-use grant for a named
// secret and redeem it in one shot, printing the value. Prefer `conductor call`
// (the value never enters the session) whenever a verb can do the work.
func cmdSecret(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: conductor secret <name>")
	}
	iss, err := skillCall(memory.IPCRequest{Op: "secret_issue", Secret: args[0]})
	if err != nil {
		return err
	}
	grant, _ := iss.Result["grant"].(string)
	if grant == "" {
		return fmt.Errorf("secret: no grant issued")
	}
	red, err := skillCall(memory.IPCRequest{Op: "secret_redeem", Grant: grant})
	if err != nil {
		return err
	}
	val, _ := red.Result["value"].(string)
	fmt.Print(val)
	return nil
}

// splitFlags separates leading positional args from trailing --key value pairs.
func splitFlags(args []string) (pos []string, flags map[string]string, err error) {
	flags = map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			pos = append(pos, a)
			continue
		}
		key := strings.TrimPrefix(a, "--")
		if eq := strings.IndexByte(key, '='); eq >= 0 {
			flags[key[:eq]] = key[eq+1:]
		} else if i+1 < len(args) {
			i++
			flags[key] = args[i]
		} else {
			flags[key] = ""
		}
	}
	return pos, flags, nil
}

func printJSON(v any) error {
	if v == nil {
		fmt.Println("{}")
		return nil
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}
