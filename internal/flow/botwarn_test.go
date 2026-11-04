package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// fakechatDecl is a minimal connector type whose only event declares
// ConversationReply (a chat/DM message) — just enough for
// WarnInteractiveBotSteps to recognize it as conversational, mirroring how a
// real chat plugin (slack/discord/teams) declares its message event.
var fakechatDecl = &connector.TypeDecl{
	Type: "fakechat",
	Desc: "Fake: an in-memory conversational connector type for flow package tests.",
	Events: []connector.EventDecl{{
		Name: "message",
		Desc: "a synthetic chat message",
		Semantics: &sdk.EventSemantics{
			ConversationReply: &sdk.ConversationReply{ID: "{{.chat.room}}", Author: "chat.user", Text: "chat.text"},
		},
	}},
}

type fakechatImpl struct{}

func (fakechatImpl) Validate() error          { return nil }
func (fakechatImpl) DeclaredEvents() []string { return nil }
func (fakechatImpl) Source([]connector.CompiledTrigger) (core.Integration, error) {
	return nil, nil
}
func (fakechatImpl) Invoke(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, nil
}

func init() {
	connector.RegisterType(fakechatDecl, func(string, config.ConnectorRef, connector.Deps) (connector.Impl, error) {
		return fakechatImpl{}, nil
	})
}

// rt2 #7: a `bot:` step wired to a conversational event with no `requester:`
// is runtime-safe (it classifies as the least trusted tier, see
// internal/bot's sourcedClass), but still worth an operator's attention — the
// boot-time warning flags it without failing config load.
func TestWarnInteractiveBotStepsFlagsMissingRequester(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
  chat: { use: fakechat }
triggers:
  - on: chat.message
    steps:
      - { bot: { name: sre, title: x } }               # no requester: — should warn
  - on: chat.message
    steps:
      - { bot: { name: sre, title: x, requester: "{{.user}}" } } # has one — silent
  - on: svc.ping
    steps:
      - { bot: { name: sre, title: x } }               # not conversational — silent
`)
	reg := buildRegistry(t, cfg)
	var got []string
	WarnInteractiveBotSteps(cfg, reg, func(msg string) { got = append(got, msg) })
	if len(got) != 1 {
		t.Fatalf("warnings: %v", got)
	}
	if !strings.Contains(got[0], "triggers[0]") || !strings.Contains(got[0], "chat.message") {
		t.Fatalf("warning: %q", got[0])
	}
}

// A team-routed bot step is skipped: team assignment re-classifies per
// assignee at delivery time, so the trigger-level warning would be noise.
func TestWarnInteractiveBotStepsSkipsTeamSteps(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  chat: { use: fakechat }
teams:
  platform: { routing: { fallback: sre } }
triggers:
  - on: chat.message
    steps:
      - { bot: { team: platform, title: x } }
`)
	reg := buildRegistry(t, cfg)
	var got []string
	WarnInteractiveBotSteps(cfg, reg, func(msg string) { got = append(got, msg) })
	if len(got) != 0 {
		t.Fatalf("warnings: %v, want none (team steps are skipped)", got)
	}
}
