package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	cronint "github.com/NodeSpy/conductor/internal/integrations/cron"
	rssint "github.com/NodeSpy/conductor/internal/integrations/rss"
	webhookint "github.com/NodeSpy/conductor/internal/integrations/webhook"
	"github.com/NodeSpy/conductor/internal/netguard"
)

// lowerAction builds the trigger-identity fields every connectors-model
// source lowering carries on the legacy config.Action it emits into an
// integration — nothing else. The rest of an Action's fields are legacy
// step/dispatch config that the connectors model doesn't use; only the
// identity, enable, and shadow markers (plus the FlowRef back-pointer to the
// compiled trigger) travel through.
func lowerAction(t CompiledTrigger) config.Action {
	return config.Action{
		Name:    t.Spec.Name,
		Enabled: t.Spec.Enabled,
		Shadow:  t.Spec.Shadow,
		FlowRef: t.Ref(),
	}
}

// ---------------------------------------------------------------------------
// cron
// ---------------------------------------------------------------------------

var cronDecl = &TypeDecl{
	Type: "cron",
	Desc: "Cron: fires on a schedule (cron spec or fixed interval); no verbs.",
	Connection: Schema{
		"schedules": {Type: TMap, Required: true, Desc: "name -> { cron, every, run_on_start }"},
	},
	Events: []EventDecl{
		{
			Name: "<schedule>", Dynamic: true, Desc: "a configured schedule fired",
			Context: Schema{
				"schedule": {Type: TString},
				"kind":     {Type: TString},
				"title":    {Type: TString},
			},
		},
	},
}

func init() { RegisterType(cronDecl, newCronImpl) }

type cronSchedule struct {
	Cron       string          `yaml:"cron"`
	Every      config.Duration `yaml:"every"`
	RunOnStart bool            `yaml:"run_on_start"`
}

type cronConn struct {
	Schedules map[string]cronSchedule `yaml:"schedules"`
}

type cronImpl struct {
	name string
	conn cronConn
	deps Deps
}

func newCronImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var conn cronConn
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("connector %q: decode cron connection: %w", name, err)
	}
	return &cronImpl{name: name, conn: conn, deps: deps}, nil
}

func (c *cronImpl) Validate() error { return nil }

func (c *cronImpl) DeclaredEvents() []string {
	out := make([]string, 0, len(c.conn.Schedules))
	for k := range c.conn.Schedules {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Source lowers each trigger into its schedule's cronint.Schedule. Exactly
// one trigger may target a given schedule name — cron.Schedule carries a
// single config.Action, so a second trigger on the same schedule has nowhere
// to go.
func (c *cronImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	schedules := make([]cronint.Schedule, 0, len(triggers))
	for _, t := range triggers {
		name := t.Spec.Event()
		sched, ok := c.conn.Schedules[name]
		if !ok {
			return nil, fmt.Errorf("trigger on %s: unknown cron schedule %q (declared: %s)", t.Spec.On, name, strings.Join(c.DeclaredEvents(), ", "))
		}
		if seen[name] {
			return nil, fmt.Errorf("trigger on %s: one trigger per cron schedule — define a second schedule", t.Spec.On)
		}
		seen[name] = true
		schedules = append(schedules, cronint.Schedule{
			Name:       name,
			Cron:       sched.Cron,
			Every:      sched.Every,
			RunOnStart: sched.RunOnStart,
			Action:     lowerAction(t),
		})
	}
	return buildIntegration("cron", c.name, cronint.Config{Schedules: schedules})
}

func (c *cronImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	return nil, fmt.Errorf("cron: no verbs")
}

// ---------------------------------------------------------------------------
// webhook
// ---------------------------------------------------------------------------

var webhookDecl = &TypeDecl{
	Type: "webhook",
	Desc: "Webhook: generic inbound JSON delivery via a field-mapping DSL; plus a generic outbound HTTP post verb.",
	Connection: Schema{
		"listen":        {Type: TString, Desc: "direct HTTP listener address, e.g. :8099"},
		"smee_url":      {Type: TString, Desc: "smee.io channel (no public ingress needed)"},
		"sources":       {Type: TMap, Required: true, Desc: "name -> { path, sign: {header,secret,scheme}, match, title, dedup }"},
		"allow_private": {Type: TBool, Desc: "permit webhook.post to reach loopback/private/link-local/CGNAT addresses (default false — blocked to prevent SSRF)"},
		"allow_hosts":   {Type: TList, Desc: "specific hosts (by name) or exact IPs that webhook.post may reach even in an otherwise-blocked range"},
	},
	Events: []EventDecl{
		{
			Name: "<source>", Dynamic: true, Desc: "a configured source received a delivery",
			Context: Schema{
				"body":   {Type: TMap},
				"kind":   {Type: TString},
				"title":  {Type: TString},
				"repo":   {Type: TString},
				"number": {Type: TInt},
			},
		},
	},
	Verbs: []VerbDecl{
		{
			Name: "post", Desc: "generic outbound HTTP request",
			Options: Schema{
				"url":     {Type: TString, Required: true},
				"method":  {Type: TString, Desc: "HTTP method (default POST)"},
				"headers": {Type: TMap, Desc: "request headers"},
				"body":    {Type: TString, Desc: "raw request body"},
				"json":    {Type: TAny, Desc: "value to JSON-marshal as the body (mutually exclusive with body)"},
				"timeout": {Type: TDuration, Desc: "request timeout (default 30s)"},
			},
			Outputs: Schema{
				"status": {Type: TInt},
				"body":   {Type: TString, Desc: "response body, capped at 64KB"},
			},
		},
	},
}

func init() { RegisterType(webhookDecl, newWebhookImpl) }

type webhookSign struct {
	Header string `yaml:"header"`
	Secret string `yaml:"secret"`
	Scheme string `yaml:"scheme"`
}

type webhookSource struct {
	Path  string      `yaml:"path"`
	Sign  webhookSign `yaml:"sign"`
	Match string      `yaml:"match"`
	Title string      `yaml:"title"`
	Dedup string      `yaml:"dedup"`
}

type webhookConn struct {
	Listen       string                   `yaml:"listen"`
	SmeeURL      string                   `yaml:"smee_url"`
	Sources      map[string]webhookSource `yaml:"sources"`
	AllowPrivate bool                     `yaml:"allow_private"`
	AllowHosts   []string                 `yaml:"allow_hosts"`
}

type webhookImpl struct {
	name string
	conn webhookConn
	deps Deps
	// resolve overrides the DNS resolver in the egress guard (tests only).
	resolve func(ctx context.Context, host string) ([]net.IPAddr, error)
}

var errWebhookBlockedIP = fmt.Errorf("webhook.post: refused: resolves to a blocked address (loopback/private/link-local); set allow_private or allow_hosts to permit it")

// safeDial mirrors the callable callback poster's guarded dialer: it resolves
// the target host daemon-side and connects only to a resolved IP that passes
// netguard, so a caller-supplied url whose host resolves to loopback, cloud
// metadata (169.254.169.254), RFC1918/ULA, link-local, or CGNAT is refused by
// default. Because it dials the exact IP it just vetted (not a re-resolved
// name), DNS rebinding can't slip an internal address past the check. Operators
// opt specific targets back in via allow_private (blanket) or allow_hosts (by
// name or exact IP).
func (w *webhookImpl) safeDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []net.IPAddr
	if w.resolve != nil {
		ips, err = w.resolve(ctx, host)
	} else {
		ips, err = net.DefaultResolver.LookupIPAddr(ctx, host)
	}
	if err != nil {
		return nil, err
	}
	hostAllowed := w.conn.AllowPrivate
	ipAllowed := map[string]bool{}
	for _, h := range w.conn.AllowHosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			ipAllowed[ip.String()] = true
		} else if strings.EqualFold(h, host) {
			// A host listed by name is operator-trusted: permit whatever it
			// resolves to, even a private/LAN address.
			hostAllowed = true
		}
	}
	var blocked bool
	var d net.Dialer
	var lastErr error
	for _, ipa := range ips {
		if netguard.Blocked(ipa.IP) && !hostAllowed && !ipAllowed[ipa.IP.String()] {
			blocked = true
			continue
		}
		conn, derr := d.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), port))
		if derr == nil {
			return conn, nil
		}
		lastErr = derr
	}
	if blocked {
		return nil, errWebhookBlockedIP
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("webhook.post: no address for %q", host)
}

func newWebhookImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var conn webhookConn
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("connector %q: decode webhook connection: %w", name, err)
	}
	ctx := context.Background()
	for srcName, src := range conn.Sources {
		secret, err := deps.Secrets.Resolve(ctx, src.Sign.Secret)
		if err != nil {
			return nil, fmt.Errorf("sources.%s.sign.secret: %w", srcName, err)
		}
		if secret != "" {
			deps.Secrets.Track(secret)
		}
		src.Sign.Secret = secret
		conn.Sources[srcName] = src
	}
	return &webhookImpl{name: name, conn: conn, deps: deps}, nil
}

func (w *webhookImpl) Validate() error {
	if w.conn.Listen == "" && w.conn.SmeeURL == "" {
		return fmt.Errorf("connector %q: set listen and/or smee_url", w.name)
	}
	return nil
}

func (w *webhookImpl) DeclaredEvents() []string {
	out := make([]string, 0, len(w.conn.Sources))
	for k := range w.conn.Sources {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// webhookGroup accumulates the triggers targeting one source name, plus the
// repo they agree on (if any).
type webhookGroup struct {
	triggers []CompiledTrigger
	repo     string
}

// Source lowers the connector's triggers into a webhook integration: each
// referenced source name becomes one webhookint.Source carrying every
// trigger on it as an action variant (mirroring slack's per-event grouping).
func (w *webhookImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	groups := map[string]*webhookGroup{}
	var order []string
	for _, t := range triggers {
		name := t.Spec.Event()
		if _, ok := w.conn.Sources[name]; !ok {
			return nil, fmt.Errorf("trigger on %s: unknown webhook source %q (declared: %s)", t.Spec.On, name, strings.Join(w.DeclaredEvents(), ", "))
		}
		g, ok := groups[name]
		if !ok {
			g = &webhookGroup{}
			groups[name] = g
			order = append(order, name)
		}
		if t.Spec.Repo != "" {
			if g.repo != "" && g.repo != t.Spec.Repo {
				return nil, fmt.Errorf("trigger on %s: source %q already has repo %q from another trigger, got %q", t.Spec.On, name, g.repo, t.Spec.Repo)
			}
			g.repo = t.Spec.Repo
		}
		g.triggers = append(g.triggers, t)
	}
	sources := make([]webhookint.Source, 0, len(order))
	for _, name := range order {
		src := w.conn.Sources[name]
		g := groups[name]
		actions := make(config.ActionSet, 0, len(g.triggers))
		for _, t := range g.triggers {
			actions = append(actions, lowerAction(t))
		}
		sources = append(sources, webhookint.Source{
			Name:    name,
			Path:    src.Path,
			Sign:    webhookint.Sign{Header: src.Sign.Header, Secret: src.Sign.Secret, Scheme: src.Sign.Scheme},
			Match:   src.Match,
			Title:   src.Title,
			Dedup:   src.Dedup,
			Repo:    g.repo,
			Actions: actions,
		})
	}
	cfg := webhookint.Config{Listen: w.conn.Listen, SmeeURL: w.conn.SmeeURL, Sources: sources}
	return buildIntegration("webhook", w.name, cfg)
}

func (w *webhookImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	if verb != "post" {
		return nil, fmt.Errorf("webhook: unknown verb %q", verb)
	}
	url, _ := opts["url"].(string)
	if url == "" {
		return nil, fmt.Errorf("webhook.post: options.url is required")
	}
	method, _ := opts["method"].(string)
	if method == "" {
		method = http.MethodPost
	}
	bodyStr, _ := opts["body"].(string)
	jsonVal, hasJSON := opts["json"]
	if bodyStr != "" && hasJSON {
		return nil, fmt.Errorf("webhook.post: set options.body or options.json, not both")
	}
	var reader io.Reader
	contentType := ""
	switch {
	case hasJSON:
		b, err := json.Marshal(jsonVal)
		if err != nil {
			return nil, fmt.Errorf("webhook.post: marshal options.json: %w", err)
		}
		reader = bytes.NewReader(b)
		contentType = "application/json"
	case bodyStr != "":
		reader = strings.NewReader(bodyStr)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, fmt.Errorf("webhook.post: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if hdrs, ok := opts["headers"].(map[string]any); ok {
		for k, v := range hdrs {
			req.Header.Set(k, fmt.Sprintf("%v", v))
		}
	}
	timeout := 30 * time.Second
	d, err := toDuration(opts["timeout"])
	if err != nil {
		return nil, fmt.Errorf("webhook.post: options.timeout: %w", err)
	}
	if d > 0 {
		timeout = d
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:         w.safeDial,
			TLSHandshakeTimeout: 10 * time.Second,
			DisableKeepAlives:   true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// Never follow a redirect off the vetted host — a 30x to an
			// internal URL would sidestep the resolved-IP check entirely.
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("webhook.post: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("webhook.post: read response: %w", err)
	}
	return map[string]any{"status": resp.StatusCode, "body": string(respBody)}, nil
}

// ---------------------------------------------------------------------------
// rss
// ---------------------------------------------------------------------------

var rssDecl = &TypeDecl{
	Type: "rss",
	Desc: "RSS/Atom: polls feeds and fires on new items; no verbs.",
	Connection: Schema{
		"feeds": {Type: TMap, Required: true, Desc: "name -> { url, interval }"},
	},
	Events: []EventDecl{
		{
			Name: "<feed>", Dynamic: true, Desc: "a configured feed produced a new item",
			Filters: Schema{
				"match": {Type: TString, Desc: "case-insensitive regex over the item's title+summary"},
			},
			Context: Schema{
				"item.title":     {Type: TString},
				"item.link":      {Type: TString},
				"item.id":        {Type: TString},
				"item.summary":   {Type: TString},
				"item.published": {Type: TString},
				"url":            {Type: TString},
				"kind":           {Type: TString},
				"title":          {Type: TString},
			},
		},
	},
}

func init() {
	rssDecl.Filter = rssFilter
	RegisterType(rssDecl, newRSSImpl)
}

type rssFeed struct {
	URL      string          `yaml:"url"`
	Interval config.Duration `yaml:"interval"`
}

type rssConn struct {
	Feeds map[string]rssFeed `yaml:"feeds"`
}

type rssImpl struct {
	name string
	conn rssConn
	deps Deps
}

func newRSSImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var conn rssConn
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("connector %q: decode rss connection: %w", name, err)
	}
	return &rssImpl{name: name, conn: conn, deps: deps}, nil
}

func (r *rssImpl) Validate() error { return nil }

func (r *rssImpl) DeclaredEvents() []string {
	out := make([]string, 0, len(r.conn.Feeds))
	for k := range r.conn.Feeds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Source lowers the connector's triggers into an rss integration: each
// referenced feed name becomes one rssint.Feed carrying every trigger on it
// as an action variant. Match is left empty on the lowered feed — per-trigger
// match filtering is evaluated at the flow layer instead (see rssFilter),
// since two triggers on one feed can each want their own pattern.
func (r *rssImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	byName := map[string]config.ActionSet{}
	var order []string
	for _, t := range triggers {
		name := t.Spec.Event()
		if _, ok := r.conn.Feeds[name]; !ok {
			return nil, fmt.Errorf("trigger on %s: unknown rss feed %q (declared: %s)", t.Spec.On, name, strings.Join(r.DeclaredEvents(), ", "))
		}
		if _, ok := byName[name]; !ok {
			order = append(order, name)
		}
		byName[name] = append(byName[name], lowerAction(t))
	}
	feeds := make([]rssint.Feed, 0, len(order))
	for _, name := range order {
		f := r.conn.Feeds[name]
		feeds = append(feeds, rssint.Feed{
			Name:     name,
			URL:      f.URL,
			Interval: f.Interval,
			Match:    "",
			Actions:  byName[name],
		})
	}
	return buildIntegration("rss", r.name, rssint.Config{Feeds: feeds})
}

func (r *rssImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	return nil, fmt.Errorf("rss: no verbs")
}

// rssFilter evaluates an rss trigger's match filter against the emitted
// item's title+summary — mirroring rss.Integration.process's own
// re.MatchString(it.Title+"\n"+it.Summary) exactly, but at the trigger level:
// the lowered rssint.Feed.Match is left empty, so every item reaches the
// flow, and this filter decides per-trigger whether it fires.
func rssFilter(event string, filters map[string]any, trigCtx map[string]any) (bool, error) {
	match, _ := filters["match"].(string)
	if match == "" {
		return true, nil
	}
	re, err := regexp.Compile("(?i)" + match)
	if err != nil {
		return false, fmt.Errorf("filters.match: bad regex: %w", err)
	}
	item, _ := trigCtx["item"].(map[string]any)
	title, _ := item["title"].(string)
	summary, _ := item["summary"].(string)
	return re.MatchString(title + "\n" + summary), nil
}
