package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// The operator's takeover: a browser the operator drives themselves —
// headed, on their display — to seed what an agent may later use: a
// logged-in session for a site, or a dedicated passkey registered on it.
// It is sandboxed and allowlisted like an agent's, and its profile is as
// throwaway; only what the operator came for is kept.

// Takeover is a running takeover browser.
type Takeover struct {
	In  *Instance
	Tab *tab
	pm  *sandbox.ProxyManager
}

// StartTakeover launches a takeover browser (spec.Headed for a window) and
// opens its first tab, with cookies put in first (a saved session to
// continue from).
func StartTakeover(ctx context.Context, spec LaunchSpec, cookies []Cookie) (*Takeover, error) {
	if spec.Proxy == nil {
		spec.Proxy = sandbox.NewProxyManager(nil)
	}
	p, err := Launch(spec)
	if err != nil {
		spec.Proxy.Close()
		return nil, err
	}
	in, err := newInstance(ctx, p, spec.Dir, nil)
	if err != nil {
		p.Close()
		spec.Proxy.Close()
		return nil, err
	}
	if err := in.setCookies(ctx, cookies); err != nil {
		in.Close()
		spec.Proxy.Close()
		return nil, err
	}
	t, err := in.current(ctx)
	if err != nil {
		in.Close()
		spec.Proxy.Close()
		return nil, err
	}
	return &Takeover{In: in, Tab: t, pm: spec.Proxy}, nil
}

// Close ends the takeover browser.
func (k *Takeover) Close() {
	k.In.Close()
	k.pm.Close()
	_ = os.RemoveAll(k.In.dir)
}

// Open navigates the takeover tab.
func (k *Takeover) Open(ctx context.Context, url string) error { return k.In.navigate(ctx, k.Tab, url) }

// URL is the takeover tab's page.
func (k *Takeover) URL(ctx context.Context) string { u, _ := k.In.pageInfo(ctx, k.Tab); return u }

// Cookies are the takeover browser's cookies for one site.
func (k *Takeover) Cookies(ctx context.Context, site string) ([]Cookie, error) {
	all, err := k.In.cookies(ctx)
	if err != nil {
		return nil, err
	}
	return FilterCookies(site, all, time.Now()), nil
}

// ArmPasskey attaches an empty virtual authenticator to the takeover tab:
// the passkey the site creates on it is conductor's, not the operator's
// own (1Password does not export those, and this one is revocable alone).
func (k *Takeover) ArmPasskey(ctx context.Context) (string, error) {
	if err := k.In.conn().Call(ctx, k.Tab.session, "WebAuthn.enable", map[string]any{"enableUI": false}, nil); err != nil {
		return "", err
	}
	var a struct {
		ID string `json:"authenticatorId"`
	}
	err := k.In.conn().Call(ctx, k.Tab.session, "WebAuthn.addVirtualAuthenticator", map[string]any{"options": map[string]any{
		"protocol": "ctap2", "transport": "internal", "hasResidentKey": true,
		"hasUserVerification": true, "isUserVerified": true, "automaticPresenceSimulation": true,
	}}, &a)
	return a.ID, err
}

// TakePasskey waits (until ctx ends, or done closes) for the site to create
// a passkey for rpHost on the armed authenticator, and returns it.
func (k *Takeover) TakePasskey(ctx context.Context, authID, host string, done <-chan struct{}) (Passkey, error) {
	for {
		var got struct {
			Credentials []Passkey `json:"credentials"`
		}
		if err := k.In.conn().Call(ctx, k.Tab.session, "WebAuthn.getCredentials", map[string]any{"authenticatorId": authID}, &got); err != nil {
			return Passkey{}, err
		}
		for _, c := range got.Credentials {
			if rpFits(c.RPID, host) {
				return c, nil
			}
		}
		if len(got.Credentials) > 0 {
			b, _ := json.Marshal(got.Credentials[0].RPID)
			return Passkey{}, fmt.Errorf("the site created a passkey for %s, not %s", b, host)
		}
		select {
		case <-ctx.Done():
			return Passkey{}, fmt.Errorf("no passkey was created: %w", ctx.Err())
		case <-done:
			return Passkey{}, fmt.Errorf("no passkey was created")
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// CredentialHost is a credential's bound host (its passkey's relying party
// must cover it).
func CredentialHost(origin string) (string, error) {
	o, err := parseOrigin(origin)
	return o.host, err
}

// RPFits reports whether a passkey's relying party covers host.
func RPFits(rpID, host string) bool { return rpFits(rpID, host) }
