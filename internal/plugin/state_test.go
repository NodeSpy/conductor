package plugin

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

func TestStateStoreRoundTripTTLAndQuota(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStateStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	st.now = func() time.Time { return now }
	do := func(req sdk.HostStateRequest) sdk.HostStateResult {
		req.Instance = "gh"
		return st.Do("connectors/github", req)
	}

	if r := do(sdk.HostStateRequest{Op: "put", Key: "claims/7", Value: map[string]any{"n": 1}}); !r.OK {
		t.Fatalf("put: %+v", r)
	}
	if r := do(sdk.HostStateRequest{Op: "get", Key: "claims/7"}); !r.OK || r.Value.(map[string]any)["n"] != float64(1) {
		t.Fatalf("get: %+v", r)
	}
	// Another plugin's bucket with the same instance name is a different store.
	if r := st.Do("connectors/other", sdk.HostStateRequest{Instance: "gh", Op: "get", Key: "claims/7"}); !r.OK || r.Value != nil {
		t.Fatalf("plugins must not share state: %+v", r)
	}
	// TTL expiry.
	do(sdk.HostStateRequest{Op: "put", Key: "tmp", Value: "x", TTL: "1m"})
	now = now.Add(2 * time.Minute)
	if r := do(sdk.HostStateRequest{Op: "get", Key: "tmp"}); r.Value != nil {
		t.Fatalf("an expired entry was returned: %+v", r)
	}
	if r := do(sdk.HostStateRequest{Op: "list", Key: "claims/"}); len(r.Value.([]any)) != 1 {
		t.Fatalf("list: %+v", r)
	}
	// Quota: one value over the limit is refused.
	if r := do(sdk.HostStateRequest{Op: "put", Key: "big", Value: strings.Repeat("x", stateMaxValBytes)}); r.OK {
		t.Fatal("an oversized value was stored")
	}
	// Survives a reopen; Drop removes the instance's state.
	_ = st.Close()
	st, _ = OpenStateStore(dir)
	defer st.Close()
	if r := st.Do("connectors/github", sdk.HostStateRequest{Instance: "gh", Op: "get", Key: "claims/7"}); r.Value == nil {
		t.Fatal("state did not survive a reopen")
	}
	_ = st.Drop("connectors/github", "gh")
	if r := st.Do("connectors/github", sdk.HostStateRequest{Instance: "gh", Op: "get", Key: "claims/7"}); r.Value != nil {
		t.Fatal("Drop left state behind")
	}
}

// A plugin reads and writes state only for instances it has been handed.
func TestHostStateOnlyForServedInstances(t *testing.T) {
	st, err := OpenStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	c := NewClient(sp, Deps{State: st})
	put := func(inst string) sdk.HostStateResult {
		res, rpcErr := c.handleRequest(context.Background(), sdk.MethodHostState,
			mustJSON(sdk.HostStateRequest{Instance: inst, Op: "put", Key: "k", Value: 1}))
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		return res.(sdk.HostStateResult)
	}
	if r := put("jira"); r.OK {
		t.Fatal("state was written for an instance the plugin was never handed")
	}
	c.serve("jira")
	if r := put("jira"); !r.OK {
		t.Fatalf("state refused for a served instance: %+v", r)
	}
}

// fakeAuthProvider is a minimal AuthProvider: it has a token for exactly the
// instances it's constructed with.
type fakeAuthProvider map[string]string

func (f fakeAuthProvider) AccessToken(_ context.Context, instance string, _ bool) (string, error) {
	tok, ok := f[instance]
	if !ok {
		return "", fmt.Errorf("instance %q has no managed oauth2 auth", instance)
	}
	return tok, nil
}

// host.auth is refused for an instance with no managed auth, even when the
// plugin was handed that instance.
func TestHostAuthRefusedWithoutManagedAuth(t *testing.T) {
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	c := NewClient(sp, Deps{Auth: fakeAuthProvider{}})
	c.serve("gh")
	res, rpcErr := c.handleRequest(context.Background(), sdk.MethodHostAuth,
		mustJSON(sdk.HostAuthRequest{Instance: "gh"}))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if r := res.(sdk.HostAuthResult); r.OK {
		t.Fatalf("host.auth answered OK for an instance with no managed auth: %+v", r)
	}
}

// host.auth is instance-scoped exactly like host.state: a plugin may ask
// only for an instance it has actually been handed — asking across instances
// (even ones the SAME client serves, or ones with their own managed auth) is
// refused.
func TestHostAuthRefusedAcrossInstances(t *testing.T) {
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	c := NewClient(sp, Deps{Auth: fakeAuthProvider{"gh": "tok-gh", "jira": "tok-jira"}})
	c.serve("gh")
	ask := func(inst string) sdk.HostAuthResult {
		res, rpcErr := c.handleRequest(context.Background(), sdk.MethodHostAuth, mustJSON(sdk.HostAuthRequest{Instance: inst}))
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		return res.(sdk.HostAuthResult)
	}
	// "jira" has its own managed auth, but this plugin was never handed it.
	if r := ask("jira"); r.OK {
		t.Fatalf("host.auth answered OK for an instance this plugin was never handed: %+v", r)
	}
	// Its own instance still works.
	if r := ask("gh"); !r.OK || r.Token != "tok-gh" {
		t.Fatalf("host.auth for its own served instance = %+v", r)
	}
	// Once also handed "jira", it works too — scoping is about having been
	// SERVED, not about some fixed one-instance-per-client rule.
	c.serve("jira")
	if r := ask("jira"); !r.OK || r.Token != "tok-jira" {
		t.Fatalf("host.auth for a later-served instance = %+v", r)
	}
}
