package main

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/flow"
)

// cmdWorkflows implements `conductor workflows [ls] | review <name> | rm
// <name>` — the operator's review surface for agent-promoted workflows
// (#36 §11). A saved workflow is unreviewed at save and after every new
// version; `review` clears it for unattended reuse (dry-run it first with
// `conductor replay`). The daemon picks a review up without a restart (the
// registry reloads on file change).
func cmdWorkflows(args []string) error {
	cfg, _, err := loadConfig(args)
	if err != nil {
		return err
	}
	st, err := flow.OpenSavedStore(savedWorkflowsPath(cfg))
	if err != nil {
		return err
	}
	rest := positional(args)
	if len(rest) == 0 || rest[0] == "ls" {
		names := make([]string, 0, len(cfg.Workflows))
		for n := range cfg.Workflows {
			names = append(names, n)
		}
		if len(names) > 0 {
			sort.Strings(names)
			fmt.Println("config workflows:")
			for _, n := range names {
				fmt.Printf("  %-24s %s\n", n, cfg.Workflows[n].Description)
			}
		}
		saved := st.All()
		if len(saved) == 0 {
			fmt.Println("no saved (agent-promoted) workflows")
			return nil
		}
		fmt.Println("saved workflows:")
		for _, w := range saved {
			state := "UNREVIEWED — dry-run, then `conductor workflows review " + w.Name + "`"
			if w.Reviewed {
				state = "reviewed"
			}
			health := ""
			if w.Runs() > 0 {
				health = fmt.Sprintf(" %d/%d ok", w.Successes, w.Runs())
				if w.Deliveries > 0 || w.Reverts > 0 {
					health += fmt.Sprintf(", %d merged/%d reverted", w.Deliveries, w.Reverts)
				}
				if w.Rotting() {
					health += " [FLAGGED: rotting]"
				}
			}
			fmt.Printf("  %-24s v%-3d by %s (%s)%s — %s\n", w.Name, w.Version, orDash(w.Source.Agent), state, health, w.Description)
		}
		return nil
	}
	switch rest[0] {
	case "review":
		if len(rest) != 2 {
			return fmt.Errorf("usage: conductor workflows review <name>")
		}
		// Reviewing IS the trust decision — show exactly what is being
		// trusted (the full step list, verbatim) before marking it, never a
		// blind flip. Runs stay re-guarded by policy.agent_authored anyway.
		w, ok := st.Get(rest[1])
		if !ok {
			return fmt.Errorf("no saved workflow %q", rest[1])
		}
		fmt.Printf("workflow %q v%d — promoted by %s (trigger %s, repo %s)\n",
			w.Name, w.Version, orDash(w.Source.Agent), orDash(w.Source.Trigger), orDash(w.Source.Repo))
		if w.Description != "" {
			fmt.Printf("description: %s\n", w.Description)
		}
		fmt.Println("steps:")
		body, err := yaml.Marshal(w.Steps)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
			fmt.Println("  " + line)
		}
		if err := st.Review(rest[1]); err != nil {
			return err
		}
		fmt.Printf("workflow %q reviewed — trusted for unattended reuse (every run stays policy-guarded)\n", rest[1])
		return nil
	case "rm":
		if len(rest) != 2 {
			return fmt.Errorf("usage: conductor workflows rm <name>")
		}
		if err := st.Delete(rest[1]); err != nil {
			return err
		}
		fmt.Printf("workflow %q removed\n", rest[1])
		return nil
	}
	return fmt.Errorf("usage: conductor workflows [ls] | review <name> | rm <name>")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
