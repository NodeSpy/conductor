package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/bot"
	"github.com/NodeSpy/conductor/internal/config"
)

// Community bots: bot templates that packs ship (docs/wiki/Bot-Templates.md).
//
//	conductor bot templates [--search <words>] [--registry <dir|git url>] [--installed]
//	conductor bot template <ref> [--registry …]
//	conductor bot new <name> --from <pack instance>/<template> [--connector in=conn]… [--grant verb=tier]… [--folder f] [--proactive] [--write]
//	conductor bot grant <bot> <verb>=<tier>… [--write]
//
// `new` and `grant` are config changes, made the way every config change is
// reviewed: the change is applied to a COPY of your config beside it,
// validated there in full, and shown as a diff with what it means — practice
// mode, the read-only verbs it starts with, every widening. Nothing is
// written without --write. The dashboard asks the daemon for the same plans
// over the control socket (templates, template, template_plan, grant_plan).

// botConfigPath is the daemon's config file (set by `conductor run`), for
// the plans the control socket serves.
var botConfigPath string

// botPlan is a planned config change and what it means.
type botPlan struct {
	Bot      string            `json:"bot"`
	Template string            `json:"template"`
	Entry    string            `json:"entry,omitempty"`
	Triggers string            `json:"triggers,omitempty"`
	Diff     string            `json:"diff"`
	Valid    bool              `json:"valid"`
	Error    string            `json:"error,omitempty"`
	Review   *config.BotOrigin `json:"review,omitempty"`
	Notes    []string          `json:"notes,omitempty"`
	// Widening says the change widens what the bot may do: the dashboard
	// asks for the passkey before writing it.
	Widening bool   `json:"widening"`
	Config   string `json:"-"`
}

// validateCopy writes text beside the config, validates it in full, and
// returns the loaded copy (nil and the error when it does not load).
func validateCopy(cfgPath string, text []byte) (*config.Config, error) {
	f, err := os.CreateTemp(filepath.Dir(cfgPath), ".conductor-plan-*.yaml")
	if err != nil {
		return nil, err
	}
	p := f.Name()
	defer os.Remove(p)
	if _, err := f.Write(text); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	if err := validateConfigFile(p); err != nil {
		return nil, err
	}
	return config.Load(p)
}

func notesFor(cfg *config.Config, name string) []string {
	var out []string
	for _, n := range cfg.BotTemplateNotes() {
		if strings.HasPrefix(n, "bot "+name+":") {
			out = append(out, n)
		}
	}
	return out
}

// planNewBot instantiates a template into a new bot, on a copy.
func planNewBot(cfgPath string, d config.TemplateBotDraft, ref string) (*botPlan, error) {
	src, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	cur, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("your config does not load as it is: %w", err)
	}
	if strings.Count(ref, "/") > 1 {
		return nil, fmt.Errorf("--from %s: not an installed template — install its pack first (see `conductor bot templates --search …`: it prints the packs: block), run `conductor init`, then name it <pack instance>/<template>", ref)
	}
	lt, err := cur.Template(ref)
	if err != nil {
		return nil, err
	}
	if _, exists := cur.Bots[d.Name]; exists {
		return nil, fmt.Errorf("a bot named %q exists", d.Name)
	}
	d.Template = lt
	entry, err := config.BotEntryYAML(d)
	if err != nil {
		return nil, err
	}
	plan := &botPlan{Bot: d.Name, Template: ref, Entry: entry}
	step1, err := config.InsertTopLevel(src, "bots", entry)
	if err != nil {
		return nil, err
	}
	final := step1
	c1, verr := validateCopy(cfgPath, step1)
	if verr == nil {
		taken := map[string]string{}
		for _, tr := range c1.Triggers {
			if tr.On != "" {
				taken[tr.On] = tr.Name
			}
		}
		trig, skipped, err := config.TemplateTriggersYAML(d.Name, lt, c1.Bots[d.Name].Origin.Bindings, taken)
		if err != nil {
			return nil, err
		}
		plan.Notes = append(plan.Notes, skipped...)
		plan.Triggers = trig
		if final, err = config.InsertTopLevel(step1, "triggers", trig); err != nil {
			return nil, err
		}
		c2, err := validateCopy(cfgPath, final)
		if err != nil {
			verr = err
		} else {
			plan.Review = c2.Bots[d.Name].Origin
			plan.Notes = append(plan.Notes, notesFor(c2, d.Name)...)
		}
	}
	plan.Valid = verr == nil
	if verr != nil {
		plan.Error = verr.Error()
	}
	plan.Diff = bot.UnifiedDiff(string(src), string(final))
	plan.Config = string(final)
	plan.Widening = len(d.Grants) > 0 && anyAbove(d.Grants)
	return plan, nil
}

func anyAbove(grants map[string]string) bool {
	for _, t := range grants {
		if t != config.TierObserve && t != config.TierNever {
			return true
		}
	}
	return false
}

// planGrant sets grants on a template bot, on a copy.
func planGrant(cfgPath, name string, grants map[string]string) (*botPlan, error) {
	src, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	cur, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("your config does not load as it is: %w", err)
	}
	m, ok := cur.Bots[name]
	if !ok {
		return nil, fmt.Errorf("no bot %q", name)
	}
	if m.Origin == nil {
		return nil, fmt.Errorf("bot %s is not from a template — its verbs are its skill.verbs and autonomy.tiers", name)
	}
	lt, err := cur.Template(m.Use)
	if err != nil {
		return nil, err
	}
	if err := config.CheckGrants(lt, grants); err != nil {
		return nil, err
	}
	out, err := config.SetBotGrants(src, name, grants)
	if err != nil {
		return nil, err
	}
	plan := &botPlan{Bot: name, Template: m.Use, Widening: anyAbove(grants)}
	c, verr := validateCopy(cfgPath, out)
	plan.Valid = verr == nil
	if verr != nil {
		plan.Error = verr.Error()
	} else {
		plan.Review = c.Bots[name].Origin
		plan.Notes = notesFor(c, name)
	}
	plan.Diff = bot.UnifiedDiff(string(src), string(out))
	plan.Config = string(out)
	return plan, nil
}

// writeConfig replaces the config file with text, keeping its mode.
func writeConfig(path, text string) error {
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp := path + ".conductor-new"
	if err := os.WriteFile(tmp, []byte(text), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// templateList is the catalog: installed, then the registry.
func templateList(cfgPath, search, registry string, installedOnly, full bool) (map[string]any, error) {
	out := map[string]any{}
	var all []config.TemplateSummary
	if cfg, err := config.Load(cfgPath); err == nil {
		all = cfg.InstalledTemplates(full)
	} else {
		out["config_error"] = err.Error()
	}
	if !installedOnly {
		dir, official, err := config.RegistryDir(registry)
		if err != nil {
			out["registry_error"] = err.Error()
		} else {
			reg, problems := config.RegistryTemplates(dir, official)
			installed := map[string]bool{}
			for _, t := range all {
				installed[t.Pack+"/"+t.Template] = true
			}
			for _, t := range reg {
				if !installed[t.Pack+"/"+t.Template] {
					all = append(all, t)
				}
			}
			if len(problems) > 0 {
				out["registry_problems"] = problems
			}
		}
	}
	all = config.SearchTemplates(all, search)
	list := make([]any, 0, len(all))
	for _, t := range all {
		list = append(list, toJSONMap(t))
	}
	out["templates"] = list
	return out, nil
}

func toJSONMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// botTemplatesCmd serves the four operator commands, locally (no daemon).
func botTemplatesCmd(sub string, args []string) error {
	cfgPath, rest := configPath(args)
	loadEnvFile(filepath.Join(filepath.Dir(cfgPath), "conductor.env"))
	pos, fl, _, _ := botFlags(rest)
	multi := func(flag string) map[string]string {
		m := map[string]string{}
		for i := 0; i < len(rest); i++ {
			if rest[i] == "--"+flag && i+1 < len(rest) {
				k, v, _ := strings.Cut(rest[i+1], "=")
				m[strings.TrimSpace(k)] = strings.TrimSpace(v)
				i++
			}
		}
		return m
	}
	switch sub {
	case "templates":
		out, err := templateList(cfgPath, strings.Join(append(pos, fl["search"]), " "), fl["registry"], fl["installed"] != "", false)
		if err != nil {
			return err
		}
		if fl["json"] != "" {
			return printJSON(out)
		}
		printTemplates(out)
		return nil
	case "template":
		if len(pos) != 1 {
			return fmt.Errorf("usage: conductor bot template <ref> [--registry <dir|git url>]")
		}
		out, err := templateList(cfgPath, "", fl["registry"], false, true)
		if err != nil {
			return err
		}
		ts, _ := out["templates"].([]any)
		for _, x := range ts {
			t, _ := x.(map[string]any)
			if t["ref"] == pos[0] {
				if fl["json"] != "" {
					return printJSON(t)
				}
				printTemplate(t)
				return nil
			}
		}
		return fmt.Errorf("no template %q (`conductor bot templates` lists them)", pos[0])
	case "new":
		if len(pos) != 1 || fl["from"] == "" {
			return fmt.Errorf("usage: conductor bot new <name> --from <pack instance>/<template> [--connector input=connector]… [--grant verb=tier]… [--folder <f>] [--proactive] [--write]")
		}
		d := config.TemplateBotDraft{Name: pos[0], Connectors: multi("connector"), Grants: multi("grant"), Folder: fl["folder"], Proactive: fl["proactive"] != ""}
		plan, err := planNewBot(cfgPath, d, fl["from"])
		if err != nil {
			return err
		}
		return finishPlan(cfgPath, plan, fl)
	case "grant":
		if len(pos) < 2 {
			return fmt.Errorf("usage: conductor bot grant <bot> <verb>=<tier>… [--write]")
		}
		grants := map[string]string{}
		for _, a := range pos[1:] {
			k, v, ok := strings.Cut(a, "=")
			if !ok {
				return fmt.Errorf("grant %q: write <verb>=<tier>", a)
			}
			grants[k] = v
		}
		plan, err := planGrant(cfgPath, pos[0], grants)
		if err != nil {
			return err
		}
		return finishPlan(cfgPath, plan, fl)
	}
	return fmt.Errorf("unknown command %q", sub)
}

func finishPlan(cfgPath string, plan *botPlan, fl map[string]string) error {
	if fl["json"] != "" {
		if err := printJSON(plan); err != nil {
			return err
		}
	} else {
		printPlan(plan)
	}
	if !plan.Valid {
		return fmt.Errorf("the change does not validate — nothing written")
	}
	if fl["write"] == "" {
		fmt.Println("\nNot written: review the diff, then run the same command with --write.")
		return nil
	}
	if err := writeConfig(cfgPath, plan.Config); err != nil {
		return err
	}
	fmt.Printf("\nWritten to %s. `conductor reload` (or a restart) loads it.\n", cfgPath)
	return nil
}

func printPlan(p *botPlan) {
	fmt.Printf("Bot %s from template %s\n", p.Bot, p.Template)
	if r := p.Review; r != nil {
		var b []string
		for _, in := range sortedStrKeys(r.Bindings) {
			b = append(b, in+" → "+r.Bindings[in])
		}
		fmt.Printf("  pack:        %s %s (%s)\n", r.Pack, r.Version, r.Source)
		fmt.Printf("  connectors:  %s\n", orNoneCLI(strings.Join(b, ", ")))
		fmt.Printf("  practice:    %v (nothing above observe runs; it records what it would do)\n", r.Practice)
		fmt.Printf("  read-only:   %s\n", orNoneCLI(strings.Join(r.ReadOnly, ", ")))
		if len(r.Widenings) > 0 {
			fmt.Printf("  WIDENING:    %s\n", strings.Join(r.Widenings, "; "))
		} else {
			fmt.Printf("  widenings:   none\n")
		}
		var ug []string
		for _, v := range sortedStrKeys(r.Ungranted) {
			ug = append(ug, v+" (at most "+r.Ungranted[v]+")")
		}
		fmt.Printf("  not granted: %s\n", orNoneCLI(strings.Join(ug, ", ")))
	}
	for _, n := range p.Notes {
		if !strings.Contains(n, ": from template ") {
			fmt.Printf("  note:        %s\n", n)
		}
	}
	if p.Valid {
		fmt.Println("  validated on a copy of your config: ok")
	} else {
		fmt.Printf("  validated on a copy of your config: FAILED — %s\n", p.Error)
	}
	fmt.Println("\n--- config diff")
	fmt.Print(p.Diff)
}

func printTemplates(out map[string]any) {
	ts, _ := out["templates"].([]any)
	if len(ts) == 0 {
		fmt.Println("no bot templates found")
	}
	for _, x := range ts {
		t, _ := x.(map[string]any)
		where := "registry"
		if inst, _ := t["installed"].(bool); inst {
			where = "installed"
			if u, ok := t["used_by"].([]any); ok && len(u) > 0 {
				var names []string
				for _, n := range u {
					names = append(names, fmt.Sprint(n))
				}
				where += ", used by " + strings.Join(names, ", ")
			}
		}
		fmt.Printf("%-40v %v %v  [%s]\n    %v\n    requests %v read-only verb(s), %v more that need your grant\n", t["ref"], t["pack"], t["version"], where, t["description"], t["read_only"], t["above_observe"])
		if inst, _ := t["install"].(string); inst != "" {
			fmt.Printf("    install: %s\n", inst)
		}
	}
	for _, k := range []string{"config_error", "registry_error"} {
		if v, ok := out[k]; ok {
			fmt.Printf("(%s: %v)\n", strings.ReplaceAll(k, "_", " "), v)
		}
	}
	if p, ok := out["registry_problems"].([]any); ok {
		for _, x := range p {
			fmt.Printf("(registry: %v)\n", x)
		}
	}
}

func printTemplate(t map[string]any) {
	fmt.Printf("%v — %v %v\n%v\n", t["ref"], t["pack"], t["version"], t["description"])
	if inst, _ := t["install"].(string); inst != "" {
		fmt.Printf("install: %s\n", inst)
	}
	section := func(title, key string, line func(map[string]any) string) {
		xs, _ := t[key].([]any)
		if len(xs) == 0 {
			return
		}
		fmt.Printf("\n%s:\n", title)
		for _, x := range xs {
			m, _ := x.(map[string]any)
			fmt.Println("  " + line(m))
		}
	}
	section("connector inputs (you bind them)", "inputs", func(m map[string]any) string {
		req := "required"
		if r, _ := m["required"].(bool); !r {
			req = "optional"
		}
		s := fmt.Sprintf("%v: a %v connector", m["name"], m["type"])
		if v, _ := m["version"].(string); v != "" {
			s += " " + v
		}
		s += " (" + req + ")"
		if d, _ := m["desc"].(string); d != "" {
			s += " — " + d
		}
		return s
	})
	section("requested verbs (maximum tier — a new bot starts with the observe ones only)", "verbs", func(m map[string]any) string {
		s := fmt.Sprintf("%-40v at most %v", m["verb"], m["max"])
		if w, _ := m["why"].(string); w != "" {
			s += " — " + w
		}
		return s
	})
	section("suggested triggers (written disarmed by `conductor bot new`)", "triggers", func(m map[string]any) string {
		s := fmt.Sprintf("%v: on %v", m["name"], m["on"])
		if d, _ := m["desc"].(string); d != "" {
			s += " — " + d
		}
		return s
	})
	if p, ok := t["proactive"].(map[string]any); ok {
		fmt.Printf("\nsuggested proactive checks (with proactive: {}): every %v, %v, notify at %v\n", p["every"], p["checklist"], p["notify_at"])
	}
	if s, _ := t["model"].(string); s != "" {
		fmt.Printf("suggested model: %s\n", s)
	}
	if s, _ := t["runtime"].(string); s != "" {
		fmt.Printf("suggested runtime: %s (yours to configure)\n", s)
	}
	if xs, _ := t["seed"].([]any); len(xs) > 0 {
		fmt.Printf("\nseed memory (%d documents):", len(xs))
		for _, x := range xs {
			fmt.Printf(" %v", x)
		}
		fmt.Println()
	}
	if ch, _ := t["charter"].(string); ch != "" {
		lines := strings.Split(ch, "\n")
		if len(lines) > 12 {
			lines = append(lines[:12], "…")
		}
		fmt.Printf("\ncharter:\n  %s\n", strings.Join(lines, "\n  "))
	}
}

func orNoneCLI(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func sortedStrKeys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// templateControl serves the dashboard's template ops on the control socket.
func templateControl(conn net.Conn, req controlRequest) {
	args := req.Args
	str := func(k string) string {
		if v, ok := args[k]; ok && v != nil {
			return fmt.Sprint(v)
		}
		return ""
	}
	strMap := func(k string) map[string]string {
		out := map[string]string{}
		if m, ok := args[k].(map[string]any); ok {
			for kk, v := range m {
				out[kk] = fmt.Sprint(v)
			}
		}
		return out
	}
	if botConfigPath == "" {
		writeControlResp(conn, controlResponse{Error: "this daemon does not know its config file"})
		return
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Minute))
	var out any
	var err error
	switch req.Sub {
	case "templates":
		out, err = templateList(botConfigPath, str("search"), str("registry"), str("installed") == "true", false)
	case "template":
		var all map[string]any
		all, err = templateList(botConfigPath, "", str("registry"), false, true)
		if err == nil {
			err = fmt.Errorf("no template %q", str("ref"))
			ts, _ := all["templates"].([]any)
			for _, x := range ts {
				if t, _ := x.(map[string]any); t["ref"] == str("ref") {
					out, err = t, nil
				}
			}
		}
	case "template_plan":
		out, err = planNewBot(botConfigPath, config.TemplateBotDraft{Name: str("name"), Connectors: strMap("connectors"), Grants: strMap("grants"),
			Folder: str("folder"), Proactive: str("proactive") == "true"}, str("template"))
	case "grant_plan":
		out, err = planGrant(botConfigPath, str("bot"), strMap("grants"))
	}
	if err != nil {
		writeControlResp(conn, controlResponse{Error: err.Error()})
		return
	}
	writeControlResp(conn, controlResponse{OK: true, Result: toJSONMap(out)})
}
