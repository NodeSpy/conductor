package handoff

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Slack Web API endpoints WebAPIPoster calls. Overridable via PC_SLACK_API_URL
// (a base URL, e.g. an httptest.Server) for hermetic tests — mirrors
// internal/notify's pushoverURL/notifiarrURL pattern. Production (the env var
// unset) leaves them at the real vendor host.
var (
	slackPostMessageURL       = "https://slack.com/api/chat.postMessage"
	slackConversationsOpenURL = "https://slack.com/api/conversations.open"
)

func init() {
	if v := os.Getenv("PC_SLACK_API_URL"); v != "" {
		base := strings.TrimRight(v, "/")
		slackPostMessageURL = base + "/chat.postMessage"
		slackConversationsOpenURL = base + "/conversations.open"
	}
}

// WebAPIPoster is the real Poster/DMOpener implementation: it posts via
// chat.postMessage and opens DMs via conversations.open, authenticating with a
// Slack bot token. It needs no other state, so one instance is shared by every
// Present call a SlackChannel makes.
type WebAPIPoster struct {
	botToken string
	http     *http.Client
}

// NewWebAPIPoster builds a Poster/DMOpener authenticating with botToken (a
// Slack bot token, xoxb-…, with the chat:write scope — and, for a `to: dm`
// channel, im:write too).
func NewWebAPIPoster(botToken string) *WebAPIPoster {
	return &WebAPIPoster{botToken: botToken, http: &http.Client{Timeout: 15 * time.Second}}
}

// Post implements Poster via chat.postMessage.
func (p *WebAPIPoster) Post(ctx context.Context, channel, threadTS, text string) (string, error) {
	payload := map[string]string{"channel": channel, "text": text}
	if threadTS != "" {
		payload["thread_ts"] = threadTS
	}
	var out struct {
		OK    bool   `json:"ok"`
		TS    string `json:"ts"`
		Error string `json:"error"`
	}
	if err := p.call(ctx, slackPostMessageURL, payload, &out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", fmt.Errorf("chat.postMessage: %s", out.Error)
	}
	return out.TS, nil
}

// OpenDM implements DMOpener via conversations.open.
func (p *WebAPIPoster) OpenDM(ctx context.Context, user string) (string, error) {
	var out struct {
		OK      bool `json:"ok"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
		Error string `json:"error"`
	}
	if err := p.call(ctx, slackConversationsOpenURL, map[string]string{"users": user}, &out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", fmt.Errorf("conversations.open: %s", out.Error)
	}
	return out.Channel.ID, nil
}

// call POSTs a JSON payload to a Slack Web API method as the bot and decodes
// the JSON response into out.
func (p *WebAPIPoster) call(ctx context.Context, url string, payload map[string]string, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.botToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}
