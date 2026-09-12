package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestValidateMemorySection(t *testing.T) {
	base := func(m *MemoryConfig, stores map[string]StoreRef) *Config {
		return &Config{Memory: m, Stores: stores,
			ConnectorsMap: map[string]ConnectorRef{}, Triggers: []TriggerSpec{{On: "manual"}}}
	}
	cases := []struct {
		name    string
		m       *MemoryConfig
		stores  map[string]StoreRef
		wantErr string
	}{
		{"nil is fine", nil, nil, ""},
		{"no backend", &MemoryConfig{}, nil, "exactly one backend"},
		{"two backends", &MemoryConfig{Dir: "/x", Type: "memory"}, nil, "exactly one backend"},
		{"store and dir", &MemoryConfig{Store: "s", Dir: "/x"}, nil, "exactly one backend"},
		{"ephemeral", &MemoryConfig{Type: "memory"}, nil, ""},
		{"dir", &MemoryConfig{Dir: "~/mem"}, nil, ""},
		{"dir with explicit file type", &MemoryConfig{Dir: "/m", Type: "file"}, nil, ""},
		{"file type without dir", &MemoryConfig{Type: "file"}, nil, "exactly one backend"},
		{"unknown type", &MemoryConfig{Dir: "/m", Type: "weird"}, nil, "unknown type"},
		{"unknown store", &MemoryConfig{Store: "ghost"}, nil, "unknown store"},
		{"known store", &MemoryConfig{Store: "state"}, map[string]StoreRef{"state": {Type: "boltdb"}}, ""},
	}
	for _, c := range cases {
		err := base(c.m, c.stores).validateMemory()
		if c.wantErr == "" && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.wantErr)
		}
	}
}

func TestMemorySelectorUnmarshal(t *testing.T) {
	var p Step
	if err := yaml.Unmarshal([]byte("memory: true"), &p); err != nil || p.Memory == nil || !p.Memory.Enabled {
		t.Fatalf("bool true: %+v %v", p.Memory, err)
	}
	p = Step{}
	if err := yaml.Unmarshal([]byte("memory: false"), &p); err != nil || p.Memory == nil || p.Memory.Enabled {
		t.Fatalf("bool false: %+v %v", p.Memory, err)
	}
	p = Step{}
	if err := yaml.Unmarshal([]byte("memory: { scopes: [global, repo], tags: [ci], limit: 7 }"), &p); err != nil {
		t.Fatal(err)
	}
	sel := p.Memory
	if sel == nil || !sel.Enabled || len(sel.Scopes) != 2 || sel.Tags[0] != "ci" || sel.Limit != 7 {
		t.Fatalf("filter map: %+v", sel)
	}
	p = Step{}
	if err := yaml.Unmarshal([]byte(`memory: { scope: "${step}" }`), &p); err != nil || len(p.Memory.Scopes) != 1 || p.Memory.Scopes[0] != MemoryScopeStep {
		t.Fatalf("singular scope: %+v %v", p.Memory, err)
	}
	// Scope keys are OPAQUE: any string is a legal key (design §2).
	p = Step{}
	if err := yaml.Unmarshal([]byte(`memory: { scopes: [anything, acme/api, "${workflow}"] }`), &p); err != nil || len(p.Memory.Scopes) != 3 {
		t.Fatalf("opaque scope keys must be accepted: %+v %v", p.Memory, err)
	}
	// …except a blank one, which would silently read as the shared set.
	if err := yaml.Unmarshal([]byte("memory: { scopes: [\"   \"] }"), &Step{}); err == nil || !strings.Contains(err.Error(), "is blank") {
		t.Fatalf("blank scope: %v", err)
	}
	if err := yaml.Unmarshal([]byte("memory: 3"), &Step{}); err == nil {
		t.Fatal("non-bool non-map should error")
	}
	// A profile without memory: stays nil (no injection).
	p = Step{}
	if err := yaml.Unmarshal([]byte("model: x"), &p); err != nil || p.Memory != nil {
		t.Fatalf("absent: %+v %v", p.Memory, err)
	}
}

func TestMemoryConnectorNameReserved(t *testing.T) {
	var cfg Config
	y := "connectors:\n  memory: { use: command }\ntriggers:\n  - { on: manual, steps: [ { run: js, code: \"1\" } ] }\n"
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatal(err)
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), `"memory" is reserved`) {
		t.Fatalf("want reserved-name error, got %v", err)
	}
}
