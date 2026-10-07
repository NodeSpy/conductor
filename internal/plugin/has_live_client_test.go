package plugin

import "testing"

// TestHasLiveClient is a TEST GAP at manager.go ~331: a key with neither a
// persistent shared client NOR any per-instance (isolated) client must
// report false — the condition reloadMoved reads to decide "restart, this
// plugin has nothing swappable" (an ACP-dialect runtime, whose process is a
// per-session spawn rather than a persistent client, is the real-world case
// this exists for).
func TestHasLiveClient(t *testing.T) {
	m := &Manager{
		clients:     map[string]*Client{},
		instClients: map[string]map[string]*Client{},
	}

	if m.HasLiveClient("connectors/nothing") {
		t.Fatal("a key with no shared client and no instance clients must report false")
	}

	// A persistent shared client makes it true.
	m.clients["connectors/shared"] = &Client{}
	if !m.HasLiveClient("connectors/shared") {
		t.Fatal("a key with a shared client must report true")
	}

	// A key whose EVERY instance isolates has no shared client at all, but
	// at least one live per-instance client still makes it true.
	m.instClients["connectors/isolated"] = map[string]*Client{"a": {}}
	if !m.HasLiveClient("connectors/isolated") {
		t.Fatal("a key with at least one live instance client must report true")
	}

	// A present-but-EMPTY instance-client map (the map exists because
	// InstanceClient initialized it, but every construction attempt failed,
	// say) must still report false — len() of an empty map, not "key
	// exists in instClients at all".
	m.instClients["connectors/emptymap"] = map[string]*Client{}
	if m.HasLiveClient("connectors/emptymap") {
		t.Fatal("a key with an empty instance-client map must report false")
	}
}
