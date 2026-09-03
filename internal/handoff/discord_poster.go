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

// discordAPIBase is the Discord REST API base URL RESTPoster calls.
// Overridable via PC_DISCORD_API_URL (a base URL, e.g. an httptest.Server) for
// hermetic tests — mirrors WebAPIPoster's PC_SLACK_API_URL pattern. Production
// (the env var unset) leaves it at the real Discord host.
var discordAPIBase = "https://discord.com/api/v10"

func init() {
	if v := os.Getenv("PC_DISCORD_API_URL"); v != "" {
		discordAPIBase = strings.TrimRight(v, "/")
	}
}

// RESTPoster is the real Poster/DMOpener implementation for Discord: it posts
// via POST /channels/{id}/messages and opens DMs via POST /users/@me/channels,
// authenticating as a Discord bot. One instance is shared by every Present call
// a DiscordChannel makes.
type RESTPoster struct {
	botToken string
	http     *http.Client
}

// NewRESTPoster builds a Poster/DMOpener authenticating with botToken (a
// Discord bot token — the bot needs the MESSAGE CONTENT privileged intent
// enabled to read replies, and must be invited to the server/channel or share
// a DM with the target user).
func NewRESTPoster(botToken string) *RESTPoster {
	return &RESTPoster{botToken: botToken, http: &http.Client{Timeout: 15 * time.Second}}
}

// Post implements Poster: it sends text to channel via POST
// /channels/{id}/messages and returns the new message's id. threadTS is
// accepted to satisfy the Poster interface but unused — Discord hand-offs
// address a channel id directly (a thread's own channel id, or a DM channel
// id), never a separate thread-timestamp field the way Slack does.
func (p *RESTPoster) Post(ctx context.Context, channel, threadTS, text string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := p.call(ctx, http.MethodPost, "/channels/"+channel+"/messages", map[string]string{"content": text}, &out); err != nil {
		return "", fmt.Errorf("discord: post message: %w", err)
	}
	return out.ID, nil
}

// OpenDM implements DMOpener via POST /users/@me/channels, which is idempotent
// — repeated calls for the same recipient return the same DM channel id.
func (p *RESTPoster) OpenDM(ctx context.Context, user string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := p.call(ctx, http.MethodPost, "/users/@me/channels", map[string]string{"recipient_id": user}, &out); err != nil {
		return "", fmt.Errorf("discord: open dm: %w", err)
	}
	return out.ID, nil
}

// call issues an authenticated JSON request against the Discord REST API and
// decodes the response into out.
func (p *RESTPoster) call(ctx context.Context, method, path string, payload map[string]string, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, discordAPIBase+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+p.botToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		if errBody.Message != "" {
			return fmt.Errorf("%s %s: %s (%d)", method, path, errBody.Message, resp.StatusCode)
		}
		return fmt.Errorf("%s %s: unexpected status %d", method, path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
