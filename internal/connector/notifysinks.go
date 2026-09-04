package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// The notify-sink connector types: ntfy, pushover, notifiarr. They are the
// verb face of what the legacy notify: block posted to — the migration maps
// each configured sink onto one of these connectors plus a notify via: route,
// and workflows can call them directly from steps and hooks too. Endpoints
// honor the same PC_* test overrides the legacy posters used, so the hermetic
// harness drives both paths identically.

var ntfyDecl = &TypeDecl{
	Type: "ntfy",
	Desc: "ntfy.sh (or self-hosted): publish messages to a topic.",
	Connection: Schema{
		"server": {Type: TString, Desc: "server base URL (default https://ntfy.sh)"},
		"topic":  {Type: TString, Desc: "default topic for publish"},
	},
	Verbs: []VerbDecl{{
		Name: "publish", Desc: "publish a message to a topic",
		Options: Schema{
			"topic":   {Type: TString, Desc: "topic (default: the connection's)"},
			"title":   {Type: TString, Desc: "the Title header"},
			"message": {Type: TString, Required: true},
		},
		Outputs: Schema{"ok": {Type: TBool}},
	}},
}

var pushoverDecl = &TypeDecl{
	Type: "pushover",
	Desc: "Pushover: message API notifications.",
	Connection: Schema{
		"token": {Type: TString, Required: true, Desc: "application token"},
		"user":  {Type: TString, Required: true, Desc: "user/group key"},
	},
	Verbs: []VerbDecl{{
		Name: "notify", Desc: "send a message",
		Options: Schema{
			"message": {Type: TString, Required: true},
			"title":   {Type: TString},
		},
		Outputs: Schema{"ok": {Type: TBool}},
	}},
}

var notifiarrDecl = &TypeDecl{
	Type: "notifiarr",
	Desc: "Notifiarr passthrough: relays to a Discord channel on Notifiarr's side.",
	Connection: Schema{
		"api_key":    {Type: TString, Required: true},
		"channel_id": {Type: TString, Desc: "default Discord channel id override"},
	},
	Verbs: []VerbDecl{{
		Name: "notify", Desc: "send a passthrough notification",
		Options: Schema{
			"text":       {Type: TString, Required: true},
			"channel_id": {Type: TString, Desc: "Discord channel id (default: the connection's)"},
		},
		Outputs: Schema{"ok": {Type: TBool}},
	}},
}

func init() {
	RegisterType(ntfyDecl, newNtfyImpl)
	RegisterType(pushoverDecl, newPushoverImpl)
	RegisterType(notifiarrDecl, newNotifiarrImpl)
}

// --- ntfy ---

type ntfyConn struct {
	Server string `yaml:"server"`
	Topic  string `yaml:"topic"`
}

type ntfyImpl struct {
	name  string
	conn  ntfyConn
	httpc *http.Client
}

func newNtfyImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var conn ntfyConn
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("connector %q: decode ntfy connection: %w", name, err)
	}
	return &ntfyImpl{name: name, conn: conn, httpc: &http.Client{Timeout: 10 * time.Second}}, nil
}

func (n *ntfyImpl) Validate() error          { return nil }
func (n *ntfyImpl) DeclaredEvents() []string { return nil }
func (n *ntfyImpl) Source([]CompiledTrigger) (core.Integration, error) {
	return nil, nil
}

// ntfyServer resolves the base URL, honoring the harness override the legacy
// poster used (PC_NTFY_DEFAULT_URL replaces the https://ntfy.sh default).
func (n *ntfyImpl) ntfyServer() string {
	if n.conn.Server != "" {
		return strings.TrimRight(n.conn.Server, "/")
	}
	if v := os.Getenv("PC_NTFY_DEFAULT_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://ntfy.sh"
}

func (n *ntfyImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	if verb != "publish" {
		return nil, fmt.Errorf("ntfy: unknown verb %q", verb)
	}
	topic, _ := opts["topic"].(string)
	if topic == "" {
		topic = n.conn.Topic
	}
	if topic == "" {
		return nil, fmt.Errorf("ntfy.publish: no topic (set options.topic or the connection's topic:)")
	}
	message, _ := opts["message"].(string)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.ntfyServer()+"/"+topic, strings.NewReader(message))
	if err != nil {
		return nil, err
	}
	if title, _ := opts["title"].(string); title != "" {
		req.Header.Set("Title", title)
	}
	resp, err := n.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ntfy.publish: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("ntfy.publish: HTTP %d", resp.StatusCode)
	}
	return map[string]any{"ok": true}, nil
}

// --- pushover ---

type pushoverConn struct {
	Token string `yaml:"token"`
	User  string `yaml:"user"`
}

type pushoverImpl struct {
	name  string
	conn  pushoverConn
	httpc *http.Client
}

func newPushoverImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var conn pushoverConn
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("connector %q: decode pushover connection: %w", name, err)
	}
	ctx := context.Background()
	var err error
	if conn.Token, err = deps.Secrets.Resolve(ctx, conn.Token); err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	if conn.User, err = deps.Secrets.Resolve(ctx, conn.User); err != nil {
		return nil, fmt.Errorf("user: %w", err)
	}
	for _, s := range []string{conn.Token, conn.User} {
		if s != "" {
			deps.Secrets.Track(s)
		}
	}
	return &pushoverImpl{name: name, conn: conn, httpc: &http.Client{Timeout: 10 * time.Second}}, nil
}

func (p *pushoverImpl) Validate() error {
	if p.conn.Token == "" || p.conn.User == "" {
		return fmt.Errorf("connector %q: token and user are required", p.name)
	}
	return nil
}
func (p *pushoverImpl) DeclaredEvents() []string { return nil }
func (p *pushoverImpl) Source([]CompiledTrigger) (core.Integration, error) {
	return nil, nil
}

// pushoverEndpoint honors the harness override PC_PUSHOVER_URL.
func pushoverEndpoint() string {
	if v := os.Getenv("PC_PUSHOVER_URL"); v != "" {
		return v
	}
	return "https://api.pushover.net/1/messages.json"
}

func (p *pushoverImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	if verb != "notify" {
		return nil, fmt.Errorf("pushover: unknown verb %q", verb)
	}
	message, _ := opts["message"].(string)
	form := url.Values{
		"token":   {p.conn.Token},
		"user":    {p.conn.User},
		"message": {message},
	}
	if title, _ := opts["title"].(string); title != "" {
		form.Set("title", title)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pushoverEndpoint(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pushover.notify: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("pushover.notify: HTTP %d", resp.StatusCode)
	}
	return map[string]any{"ok": true}, nil
}

// --- notifiarr ---

type notifiarrConn struct {
	APIKey    string `yaml:"api_key"`
	ChannelID string `yaml:"channel_id"`
}

type notifiarrImpl struct {
	name  string
	conn  notifiarrConn
	httpc *http.Client
}

func newNotifiarrImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var conn notifiarrConn
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("connector %q: decode notifiarr connection: %w", name, err)
	}
	var err error
	if conn.APIKey, err = deps.Secrets.Resolve(context.Background(), conn.APIKey); err != nil {
		return nil, fmt.Errorf("api_key: %w", err)
	}
	if conn.APIKey != "" {
		deps.Secrets.Track(conn.APIKey)
	}
	return &notifiarrImpl{name: name, conn: conn, httpc: &http.Client{Timeout: 10 * time.Second}}, nil
}

func (n *notifiarrImpl) Validate() error {
	if n.conn.APIKey == "" {
		return fmt.Errorf("connector %q: api_key is required", n.name)
	}
	return nil
}
func (n *notifiarrImpl) DeclaredEvents() []string { return nil }
func (n *notifiarrImpl) Source([]CompiledTrigger) (core.Integration, error) {
	return nil, nil
}

// notifiarrEndpoint mirrors the legacy poster's URL handling: the
// PC_NOTIFIARR_URL override is used as-is when it carries a %s slot, else the
// passthrough path is appended.
func notifiarrEndpoint(apiKey string) string {
	pattern := "https://notifiarr.com/api/v1/notification/passthrough/%s"
	if v := os.Getenv("PC_NOTIFIARR_URL"); v != "" {
		if strings.Contains(v, "%s") {
			pattern = v
		} else {
			pattern = strings.TrimRight(v, "/") + "/api/v1/notification/passthrough/%s"
		}
	}
	return fmt.Sprintf(pattern, apiKey)
}

func (n *notifiarrImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	if verb != "notify" {
		return nil, fmt.Errorf("notifiarr: unknown verb %q", verb)
	}
	text, _ := opts["text"].(string)
	channel := n.conn.ChannelID
	if c, _ := opts["channel_id"].(string); c != "" {
		channel = c
	}
	discord := map[string]any{
		"text": map[string]string{"description": text},
	}
	if channel != "" {
		discord["ids"] = map[string]string{"channel": channel}
	}
	body, _ := json.Marshal(map[string]any{
		"notification": map[string]string{"name": "conductor"},
		"discord":      discord,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, notifiarrEndpoint(n.conn.APIKey), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", n.conn.APIKey)
	resp, err := n.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("notifiarr.notify: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("notifiarr.notify: HTTP %d", resp.StatusCode)
	}
	return map[string]any{"ok": true}, nil
}

// postIncomingWebhook POSTs a JSON payload to a Slack/Discord incoming
// webhook — the post-only transport the legacy notify sinks used, kept
// byte-identical so migration changes nothing on the wire.
func postIncomingWebhook(ctx context.Context, hc *http.Client, url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webhook post: HTTP %d", resp.StatusCode)
	}
	return nil
}
