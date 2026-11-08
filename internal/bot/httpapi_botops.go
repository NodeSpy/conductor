package bot

// A bot's operator actions on the operator API (the dashboard's bot page ⋯
// menu): rotate its home session, start a proactive sweep now, deliver an
// event to it, and list its workers. Each is the control socket's operator op
// of the same name (OperatorOp), so the CLI and the dashboard share one path.
//
//	POST /v1/bots/{bot}/rotate    → the bot's status after the rotation
//	POST /v1/bots/{bot}/sweep     → { sweep, worker, status }
//	POST /v1/bots/{bot}/deliver   JSON {title, text, urgent, requester: "operator"|"event", operator} → { case, ticket }
//	GET  /v1/bots/{bot}/workers   → { workers: [...] }

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// registerBotOpRoutes adds a bot's operator actions to the operator API mux.
func (s *Service) registerBotOpRoutes(mux *Routes) {
	mux.HandleFunc("POST /v1/bots/{bot}/rotate", s.apiBotRotate)
	mux.HandleFunc("POST /v1/bots/{bot}/sweep", s.apiBotSweep)
	mux.HandleFunc("POST /v1/bots/{bot}/deliver", s.apiBotDeliver)
	mux.HandleFunc("GET /v1/bots/{bot}/workers", s.apiBotWorkers)
}

// knownBot answers 404 for a bot that isn't configured.
func (s *Service) knownBot(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("bot")
	if _, ok := s.Cfg().Bots[name]; !ok {
		apiError(w, http.StatusNotFound, fmt.Errorf("unknown bot %q", name))
		return "", false
	}
	return name, true
}

// botOpError is an operator op's refusal: the bot can't do it now (no home
// launcher, no proactive block, a sweep already running, quiet sandbox), said
// as a 409 with the op's own words.
func botOpError(w http.ResponseWriter, err error) {
	apiError(w, http.StatusConflict, err)
}

func (s *Service) apiBotRotate(w http.ResponseWriter, r *http.Request) {
	name, ok := s.knownBot(w, r)
	if !ok {
		return
	}
	out, err := s.OperatorOp(r.Context(), "rotate", map[string]any{"bot": name})
	if err != nil {
		botOpError(w, err)
		return
	}
	apiJSON(w, http.StatusOK, out)
}

func (s *Service) apiBotSweep(w http.ResponseWriter, r *http.Request) {
	name, ok := s.knownBot(w, r)
	if !ok {
		return
	}
	out, err := s.OperatorOp(r.Context(), "sweep", map[string]any{"bot": name})
	if err != nil {
		botOpError(w, err)
		return
	}
	apiJSON(w, http.StatusAccepted, out)
}

// apiBotDeliver hands the bot an event, as `conductor bot deliver` does: a
// title, optional text (fenced as untrusted data), and who asked — the
// signed-in operator, or a manual event with no person behind it.
func (s *Service) apiBotDeliver(w http.ResponseWriter, r *http.Request) {
	name, ok := s.knownBot(w, r)
	if !ok {
		return
	}
	args, ok := jsonArgs(w, r)
	if !ok {
		return
	}
	title := strings.TrimSpace(str(args, "title"))
	if title == "" {
		apiError(w, http.StatusBadRequest, errors.New("give the event a title"))
		return
	}
	var requester string
	switch str(args, "requester") {
	case "", "operator":
		a, err := s.operatorActor(str(args, "operator"))
		if err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		requester = "operator"
		if a.Operator != "" {
			requester = "operator:" + a.Operator
		}
	case "event":
		requester = "event:manual"
	default:
		apiError(w, http.StatusBadRequest, fmt.Errorf("requester is operator or event, not %q", str(args, "requester")))
		return
	}
	out, err := s.OperatorOp(r.Context(), "deliver", map[string]any{"bot": name, "title": title,
		"text": str(args, "text"), "urgent": boolArg(args, "urgent"), "requester": requester})
	if err != nil {
		botOpError(w, err)
		return
	}
	if id, _ := out["case"].(string); id != "" {
		if c := s.Ledger.Bot(name).Cases[id]; c != nil && c.Ticket != "" {
			out["ticket"] = c.Ticket
		}
	}
	apiJSON(w, http.StatusCreated, out)
}

func (s *Service) apiBotWorkers(w http.ResponseWriter, r *http.Request) {
	name, ok := s.knownBot(w, r)
	if !ok {
		return
	}
	out, err := s.OperatorOp(r.Context(), "workers", map[string]any{"bot": name})
	if err != nil {
		botOpError(w, err)
		return
	}
	if out["workers"] == nil {
		out["workers"] = []any{}
	}
	apiJSON(w, http.StatusOK, out)
}
