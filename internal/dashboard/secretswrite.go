package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/dashboard/auth"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// The Secrets screen's only paths that touch a vault entry: store (or
// replace) a value, and delete an entry. Both go through the running
// daemon's own vault registry (internal/vaults) — the backends the daemon
// opened at config load, the same ones every {{ vault ... }} resolution
// uses — and never through config staging. A value is write-only: it is read
// once from the request, handed to vaults.Write (which also puts it on the
// redaction list), and never returned, logged or audited. Both need a
// step-up (auth.PurposeSecretWrite), bound to the exact vault and key (and,
// for a write, the value's length) so a token confirmed for one entry can't
// be replayed against another, and both are audited without the value.

type secretWriteRequest struct {
	Vault string `json:"vault"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

type secretDeleteRequest struct {
	Vault string `json:"vault"`
	Key   string `json:"key"`
}

// secretKeyRE is the shape of a vault key the dashboard will write: the same
// characters the add-a-secret form allows (letters, digits, . _ / -).
var secretKeyRE = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,200}$`)

func secretWriteDigest(vault, key string, length int) string {
	b, _ := json.Marshal(map[string]any{"v": "1", "ns": "conductor-dashboard-secret-write", "vault": vault, "key": key, "length": length})
	return auth.DigestOf(b)
}

func secretDeleteDigest(vault, key string) string {
	b, _ := json.Marshal(map[string]any{"v": "1", "ns": "conductor-dashboard-secret-delete", "vault": vault, "key": key})
	return auth.DigestOf(b)
}

// liveVault resolves a vault from the daemon's registry, answering the
// request itself (and returning nil) when it can't be used. A vault that is
// in the config but not in the registry hasn't been loaded by this daemon
// yet (a staged vault not applied); it is never opened here on the side.
func (s *Server) liveVault(w http.ResponseWriter, name string) vaults.Backend {
	if !secretKeyRE.MatchString(name) {
		writeError(w, http.StatusBadRequest, "bad_request", "a vault name is required", "")
		return nil
	}
	if reason := vaults.Broken(name); reason != "" {
		writeError(w, http.StatusConflict, "vault_unreachable", fmt.Sprintf("the vault %s can't be reached: %s", name, reason), "fix how it unlocks, then try again")
		return nil
	}
	b, err := vaults.Use(name)
	if err != nil {
		if vts, _ := s.liveTree()["vaults"].(map[string]any); vts[name] != nil {
			writeError(w, http.StatusConflict, "vault_not_loaded", fmt.Sprintf("the vault %s isn't open yet", name), "apply the change that adds it, then try again")
		} else {
			writeError(w, http.StatusNotFound, "no_vault", fmt.Sprintf("there is no vault named %q", name), "add the vault first")
		}
		return nil
	}
	return b
}

// stepupOr answers 401/403 (with the step-up challenge) unless the request
// carries a step-up for exactly this digest. It reports whether to proceed.
func (s *Server) stepupOr(w http.ResponseWriter, r *http.Request, digest, desc string) bool {
	if !s.authEnforced() {
		return true
	}
	sess, ok := auth.SessionFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "sign in to continue", "")
		return false
	}
	if _, err := s.auth.RequireStepup(sess, auth.PurposeSecretWrite, digest, r.Header.Get(auth.StepupHeader)); err != nil {
		s.labels.note(r, auth.PurposeSecretWrite, digest, desc)
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":  map[string]string{"code": "stepup_required", "message": err.Error(), "hint": "confirm it's you, then try again"},
			"stepup": map[string]string{"purpose": string(auth.PurposeSecretWrite), "digest": digest, "description": desc},
		})
		return false
	}
	return true
}

// handleSecretWrite is POST /api/v1/secrets/write: store a new value, or
// replace an existing one. The answer names the entry, never its value.
func (s *Server) handleSecretWrite(w http.ResponseWriter, r *http.Request) {
	var req secretWriteRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if !secretKeyRE.MatchString(req.Key) {
		writeError(w, http.StatusBadRequest, "bad_request", "a key is required: letters, digits, . _ / -", "")
		return
	}
	if req.Value == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "a value is required", "")
		return
	}
	b := s.liveVault(w, req.Vault)
	if b == nil {
		return
	}
	// Refuse a read-only vault before asking anyone to confirm anything.
	if _, ok := b.(vaults.Writer); !ok {
		writeError(w, http.StatusConflict, "read_only_vault", fmt.Sprintf("the vault %s is read-only: its values are set in the vault itself", req.Vault), "")
		return
	}
	digest := secretWriteDigest(req.Vault, req.Key, len(req.Value))
	desc := fmt.Sprintf("store a value in %s/%s (length %d; the value is never recorded)", req.Vault, req.Key, len(req.Value))
	if !s.stepupOr(w, r, digest, desc) {
		return
	}
	if err := vaults.Write(r.Context(), req.Vault, req.Key, req.Value); err != nil {
		writeError(w, http.StatusBadGateway, "vault_error", redactValue(err.Error(), req.Value), "")
		return
	}
	s.audit(r, "dashboard.secret.write", digest, "vault", req.Vault, "key", req.Key, "length", fmt.Sprint(len(req.Value)))
	writeJSON(w, http.StatusOK, api.SecretInfo{Name: req.Key, Ref: req.Vault + "/" + req.Key, Vault: req.Vault, Key: req.Key, Stored: true})
}

// handleSecretDelete is POST /api/v1/secrets/delete: remove an entry from
// its vault. Refused for a vault type that can't delete.
func (s *Server) handleSecretDelete(w http.ResponseWriter, r *http.Request) {
	var req secretDeleteRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if !secretKeyRE.MatchString(req.Key) {
		writeError(w, http.StatusBadRequest, "bad_request", "a key is required", "")
		return
	}
	b := s.liveVault(w, req.Vault)
	if b == nil {
		return
	}
	d, ok := b.(vaults.Deleter)
	if !ok {
		writeError(w, http.StatusConflict, "read_only_vault", fmt.Sprintf("the vault %s can't remove entries: remove it in the vault itself", req.Vault), "")
		return
	}
	digest := secretDeleteDigest(req.Vault, req.Key)
	if !s.stepupOr(w, r, digest, fmt.Sprintf("remove %s/%s from its vault", req.Vault, req.Key)) {
		return
	}
	if err := d.Delete(r.Context(), req.Key); err != nil {
		writeError(w, http.StatusBadGateway, "vault_error", err.Error(), "")
		return
	}
	s.audit(r, "dashboard.secret.delete", digest, "vault", req.Vault, "key", req.Key)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": req.Vault + "/" + req.Key})
}

// redactValue keeps a backend error from ever echoing the value it was given.
func redactValue(msg, value string) string {
	if value == "" {
		return msg
	}
	return strings.ReplaceAll(msg, value, "[redacted]")
}
