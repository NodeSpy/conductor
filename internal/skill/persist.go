package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// durableSaveSlack is how far a durable session's sliding expiry must move
// before the file is rewritten.
const durableSaveSlack = time.Hour

// persistedSession is one durable session on disk. The token itself is never
// written — only its SHA-256, which is enough to recognise it when the agent
// presents it again and useless to anyone who reads the file.
type persistedSession struct {
	TokenHash     string              `json:"token_sha256"`
	Expires       time.Time           `json:"expires"`
	BoundUID      uint32              `json:"bound_uid"`
	VerbCalls     int                 `json:"verb_calls,omitempty"`
	Agent         string              `json:"agent,omitempty"`
	Repo          string              `json:"repo,omitempty"`
	Trigger       string              `json:"trigger,omitempty"`
	Number        int                 `json:"number,omitempty"`
	Policy        config.SkillPolicy  `json:"policy"`
	TargetTrusted bool                `json:"target_trusted,omitempty"`
	Dispatch      string              `json:"dispatch,omitempty"`
	Bot           string              `json:"bot,omitempty"`
	BotRole       string              `json:"bot_role,omitempty"`
	CaseID        string              `json:"case_id,omitempty"`
	Context       map[string]any      `json:"context,omitempty"`
	Sem           *sdk.EventSemantics `json:"sem,omitempty"`
	Requester     string              `json:"requester,omitempty"`
	Sandbox       *SandboxBinding     `json:"sandbox,omitempty"`
}

func tokenHash(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// SetPersistPath enables durable-session persistence at path and loads any
// sessions saved there by a previous daemon (expired ones are dropped). A
// missing or corrupt file starts empty — the cost is that hand-offs launched
// before it lose conductor access, never that the daemon fails to boot.
func (b *Broker) SetPersistPath(path string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.persistPath = path
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var list []persistedSession
	if json.Unmarshal(raw, &list) != nil {
		return
	}
	now := b.now()
	for _, p := range list {
		if p.TokenHash == "" || now.After(p.Expires) {
			continue
		}
		b.restored[p.TokenHash] = session{
			id: Identity{
				Agent: p.Agent, Repo: p.Repo, Trigger: p.Trigger, Number: p.Number,
				Policy: p.Policy, TargetTrusted: p.TargetTrusted, Dispatch: p.Dispatch,
				Bot: p.Bot, BotRole: p.BotRole, CaseID: p.CaseID,
				Context: p.Context, Sem: p.Sem, Requester: p.Requester, Sandbox: p.Sandbox,
			},
			expires:   p.Expires,
			uidBound:  true,
			boundUID:  p.BoundUID,
			verbCalls: p.VerbCalls,
			durable:   true,
		}
	}
}

// RevokeDispatch drops every session minted for a dispatch — called when its
// hand-off is released, so the token dies with the work rather than at its
// expiry.
func (b *Broker) RevokeDispatch(dispatchID string) {
	if dispatchID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	dropped := false
	for tok, s := range b.sessions {
		if s.id.Dispatch == dispatchID {
			delete(b.sessions, tok)
			dropped = dropped || s.durable
		}
	}
	for h, s := range b.restored {
		if s.id.Dispatch == dispatchID {
			delete(b.restored, h)
			dropped = true
		}
	}
	if dropped {
		b.saveLocked()
	}
}

// saveLocked writes every live durable session (best-effort, atomic, 0600).
// Callers hold b.mu.
func (b *Broker) saveLocked() {
	if b.persistPath == "" {
		return
	}
	// Marshal per session: one whose identity won't encode is skipped (and
	// logged) rather than costing every other hand-off its persistence.
	var list []json.RawMessage
	type keyed struct {
		hash string
		s    session
	}
	var all []keyed
	for tok, s := range b.sessions {
		if s.durable {
			all = append(all, keyed{tokenHash(tok), s})
		}
	}
	for h, s := range b.restored {
		all = append(all, keyed{h, s})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].hash < all[j].hash })
	for _, k := range all {
		s := k.s
		one, err := json.Marshal(persistedSession{
			TokenHash: k.hash, Expires: s.expires, BoundUID: s.boundUID, VerbCalls: s.verbCalls,
			Agent: s.id.Agent, Repo: s.id.Repo, Trigger: s.id.Trigger, Number: s.id.Number,
			Policy: s.id.Policy, TargetTrusted: s.id.TargetTrusted, Dispatch: s.id.Dispatch,
			Bot: s.id.Bot, BotRole: s.id.BotRole, CaseID: s.id.CaseID,
			Context: s.id.Context, Sem: s.id.Sem, Requester: s.id.Requester, Sandbox: s.id.Sandbox,
		})
		if err != nil {
			if b.log != nil {
				b.log("skill: hand-off session for %s#%d not persisted: %v", s.id.Repo, s.id.Number, err)
			}
			continue
		}
		list = append(list, one)
	}
	if list == nil {
		list = []json.RawMessage{}
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return
	}
	tmp := b.persistPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		if b.log != nil {
			b.log("skill: persist hand-off sessions: %v", err)
		}
		return
	}
	_ = os.Rename(tmp, b.persistPath)
}
