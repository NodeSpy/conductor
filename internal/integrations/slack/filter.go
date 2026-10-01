package slack

import "strings"

// FilterMatch evaluates ONE slack filter key against an event context
// ({"slack": {...}}). It is the single implementation both halves use: the
// flow runner's filter evaluation (connector.slackDecl.Filter delegates
// here) and the integration's own pre-match for a form trigger, which must
// decide synchronously — inside Slack's 3-second trigger_id window — whether
// to open the modal at all.
func FilterMatch(event string, filters, trigCtx map[string]any) (bool, error) {
	sc, _ := trigCtx["slack"].(map[string]any)
	get := func(k string) string {
		if sc == nil {
			return ""
		}
		v, _ := sc[k].(string)
		return v
	}
	if want, _ := filters["channel"].(string); want != "" && want != get("channel") {
		return false, nil
	}
	if users := toStrings(filters["users"]); len(users) > 0 {
		if !containsFold(users, get("user")) {
			return false, nil
		}
	}
	switch event {
	case "reaction_added":
		if want, _ := filters["reaction"].(string); want != "" && want != get("reaction") {
			return false, nil
		}
	case "slash_command":
		if want, _ := filters["command"].(string); want != "" && want != get("command") {
			return false, nil
		}
	case "message_shortcut":
		if want, _ := filters["callback_id"].(string); want != "" && want != get("callback_id") {
			return false, nil
		}
	}
	return true, nil
}

func containsFold(list []string, s string) bool {
	if s == "" {
		return false
	}
	for _, e := range list {
		if strings.EqualFold(e, s) {
			return true
		}
	}
	return false
}

func toStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if x != "" {
			return []string{x}
		}
	}
	return nil
}
