package vaults

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HashicorpVault talks to HashiCorp Vault's KV v2 engine over its HTTP API
// (no SDK dependency — the API is three small JSON endpoints). A key is
// `path/to/secret` or `path/to/secret#field`; the field defaults to "value".
// Writable: a write reads the secret's current data first and merges the one
// field, so sibling fields survive.
type HashicorpVault struct {
	Addr      string // https://vault.internal
	Mount     string // KV v2 mount, default "secret"
	Namespace string // optional X-Vault-Namespace
	Token     string // resolved unlock token
	Client    *http.Client

	c cache
}

func (h *HashicorpVault) mount() string {
	if h.Mount == "" {
		return "secret"
	}
	return strings.Trim(h.Mount, "/")
}

func (h *HashicorpVault) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// splitKey separates the secret path from the field (default "value").
func splitKey(key string) (path, field string) {
	if p, f, ok := strings.Cut(key, "#"); ok && f != "" {
		return strings.Trim(p, "/"), f
	}
	return strings.Trim(key, "/"), "value"
}

func (h *HashicorpVault) do(ctx context.Context, method, path string, body any) (map[string]any, error) {
	if h.Addr == "" {
		return nil, fmt.Errorf("hashicorp: addr: is not set")
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(h.Addr, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", h.Token)
	if h.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", h.Namespace)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var ve struct {
			Errors []string `json:"errors"`
		}
		_ = json.Unmarshal(raw, &ve)
		reason := strings.Join(ve.Errors, "; ")
		if reason == "" {
			reason = strings.TrimSpace(string(raw))
		}
		return nil, fmt.Errorf("hashicorp: HTTP %d: %s", resp.StatusCode, reason)
	}
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("hashicorp: bad response: %w", err)
		}
	}
	return out, nil
}

// data returns a KV v2 secret's current field map (nil when absent).
func (h *HashicorpVault) data(ctx context.Context, path string) (map[string]any, error) {
	out, err := h.do(ctx, http.MethodGet, "/v1/"+h.mount()+"/data/"+path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, err
	}
	d1, _ := out["data"].(map[string]any)
	d2, _ := d1["data"].(map[string]any)
	return d2, nil
}

func (h *HashicorpVault) Read(ctx context.Context, key string) (string, error) {
	if v, ok := h.c.get(key); ok {
		return v, nil
	}
	path, field := splitKey(key)
	data, err := h.data(ctx, path)
	if err != nil {
		return "", err
	}
	raw, ok := data[field]
	if !ok {
		return "", fmt.Errorf("hashicorp: no field %q at %s/%s", field, h.mount(), path)
	}
	v := fmt.Sprint(raw)
	h.c.put(key, v)
	return v, nil
}

func (h *HashicorpVault) Write(ctx context.Context, key, value string) error {
	path, field := splitKey(key)
	data, err := h.data(ctx, path)
	if err != nil {
		return err
	}
	if data == nil {
		data = map[string]any{}
	}
	data[field] = value
	if _, err := h.do(ctx, http.MethodPost, "/v1/"+h.mount()+"/data/"+path, map[string]any{"data": data}); err != nil {
		return err
	}
	h.c.put(key, value)
	return nil
}
