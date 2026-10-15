package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// Credentials: conductor signs an agent in without the agent ever seeing a
// secret. The agent names a credential the operator bound to its identity
// and to an origin; conductor checks the page, fetches the value from its
// vault (1Password, the conductor vault, pass, …) for this one fill, and
// sets it in the field from an isolated world — past the agent, past the
// page's own scripts' prototypes, never in any reply. Afterwards every
// value filled in that browser is scrubbed from what the agent gets back
// (snapshots, page text, titles, URLs), masked in screenshots, refused in
// downloads, and evaluate is off for the rest of that browser's life.

// SecretRef names a value in one of the operator's vaults.
type SecretRef struct {
	Vault string `json:"vault"`
	Key   string `json:"key"`
}

// Credential is one sign-in the operator shares with identities.
type Credential struct {
	Name string
	// Origin is the exact scheme://host[:port] (and optional path prefix)
	// every fill must happen on.
	Origin string
	// Identities may use it: exact, or a prefix ending in "*".
	Identities []string
	// LoginURL is where `login` starts when the page is elsewhere (on
	// Origin).
	LoginURL string
	// Username / Password / OTP / Passkey are where the values live.
	Username, Password, OTP, Passkey *SecretRef
	// Approval: the site asks for an approval on the operator's phone after
	// sign-in; conductor tells the operator and waits.
	Approval bool
}

// For reports whether the credential is bound to identity.
func (c Credential) For(identity string) bool {
	return LoginSite{Identities: c.Identities}.For(identity)
}

// origin is a parsed credential origin.
type origin struct{ scheme, host, port, path string }

func parseOrigin(s string) (origin, error) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return origin{}, fmt.Errorf("browser: credential origin %q is not scheme://host[:port][/path]", s)
	}
	o := origin{scheme: u.Scheme, host: strings.ToLower(u.Hostname()), port: u.Port(), path: u.Path}
	if o.port == "" {
		o.port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	return o, nil
}

// CheckOrigin refuses a credential origin that is not exact.
func CheckOrigin(s string) error { _, err := parseOrigin(s); return err }

func (o origin) String() string {
	return o.scheme + "://" + net.JoinHostPort(o.host, o.port) + o.path
}

// check refuses a page that is not exactly this origin: the browser's own
// view of the frame's security origin (never the page's word) and its URL's
// path under the bound path.
func (o origin) check(securityOrigin, pageURL string) error {
	so, err := parseOrigin(securityOrigin)
	if err != nil || so.scheme != o.scheme || so.host != o.host || so.port != o.port {
		return fmt.Errorf("the page's origin %s is not the credential's %s", redactURL(securityOrigin), o.scheme+"://"+net.JoinHostPort(o.host, o.port))
	}
	if o.path != "" && o.path != "/" {
		u, err := url.Parse(pageURL)
		if err != nil || !(u.Path == o.path || strings.HasPrefix(u.Path, strings.TrimSuffix(o.path, "/")+"/")) {
			return fmt.Errorf("the page %s is not under the credential's path %s", redactURL(pageURL), o.path)
		}
	}
	return nil
}

// Passkey is a credential's dedicated passkey: created by conductor's
// virtual authenticator at a one-time registration, its private key kept
// in the operator's vault.
type Passkey struct {
	CredentialID string `json:"credentialId"`
	RPID         string `json:"rpId"`
	PrivateKey   string `json:"privateKey"` // base64 PKCS#8
	UserHandle   string `json:"userHandle,omitempty"`
	SignCount    int    `json:"signCount"`
}

// rpFits reports whether a passkey's relying party covers host (WebAuthn's
// own rule: the RP ID is the host or a parent domain of it).
func rpFits(rpID, host string) bool {
	rpID, host = strings.ToLower(rpID), strings.ToLower(host)
	return rpID != "" && (host == rpID || strings.HasSuffix(host, "."+rpID))
}

// frame is a tab's main frame as the browser reports it.
type frame struct {
	ID             string `json:"id"`
	URL            string `json:"url"`
	SecurityOrigin string `json:"securityOrigin"`
}

func (in *Instance) mainFrame(ctx context.Context, t *tab) (frame, error) {
	var r struct {
		FrameTree struct {
			Frame frame `json:"frame"`
		} `json:"frameTree"`
	}
	if err := in.conn().Call(ctx, t.session, "Page.getFrameTree", nil, &r); err != nil {
		return frame{}, err
	}
	return r.FrameTree.Frame, nil
}

// world is a fresh isolated world in a frame: conductor's own JS globals,
// untouched by the page's scripts, sharing only the DOM.
func (in *Instance) world(ctx context.Context, t *tab, frameID string) (int, error) {
	var r struct {
		ID int `json:"executionContextId"`
	}
	err := in.conn().Call(ctx, t.session, "Page.createIsolatedWorld", map[string]any{"frameId": frameID, "worldName": "conductor"}, &r)
	return r.ID, err
}

// resolveIn resolves a node in an execution context: a node of another
// frame's document does not resolve there.
func (in *Instance) resolveIn(ctx context.Context, t *tab, id int64, ctxID int) (string, error) {
	var r struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	if err := in.conn().Call(ctx, t.session, "DOM.resolveNode", map[string]any{"backendNodeId": id, "executionContextId": ctxID}, &r); err != nil {
		return "", err
	}
	if r.Object.ObjectID == "" {
		return "", fmt.Errorf("no object")
	}
	return r.Object.ObjectID, nil
}

// callIn runs a function in an execution context (no element).
func (in *Instance) callIn(ctx context.Context, t *tab, ctxID int, fn string, args ...any) (json.RawMessage, error) {
	a := []map[string]any{}
	for _, v := range args {
		a = append(a, map[string]any{"value": v})
	}
	var r struct {
		Result struct {
			Value    json.RawMessage `json:"value"`
			ObjectID string          `json:"objectId"`
		} `json:"result"`
		Exception *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := in.conn().Call(ctx, t.session, "Runtime.callFunctionOn", map[string]any{
		"functionDeclaration": fn, "executionContextId": ctxID, "arguments": a, "returnByValue": true,
	}, &r); err != nil {
		return nil, err
	}
	if r.Exception != nil {
		return nil, fmt.Errorf("%s", r.Exception.Text)
	}
	return r.Result.Value, nil
}

// fieldCheck is conductor's check that an element can take a field: run in
// the isolated world, on the element.
const fieldCheck = `function(field){
	if (this.ownerDocument !== document || window !== window.top) return "the field is not in the page's own document (an embedded frame)";
	if (!(this instanceof HTMLInputElement)) return "it is not an input field";
	const t = (this.getAttribute("type") || "text").toLowerCase();
	if (this.disabled || this.readOnly) return "the field is disabled or read-only";
	if (field === "password") return t === "password" ? "" : "it is not a password field (type=" + t + ")";
	if (t === "password") return "a password field takes only the password";
	if (field === "otp") return ["text", "tel", "number"].includes(t) ? "" : "it is not a code field (type=" + t + ")";
	return ["text", "email", "tel"].includes(t) ? "" : "it is not a username field (type=" + t + ")";
}`

// fieldSet sets a field's value through the isolated world's own (pristine)
// setter and tells the page it changed.
const fieldSet = `function(v){
	const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set;
	this.focus();
	set.call(this, v);
	this.dispatchEvent(new InputEvent("input", {bubbles: true, inputType: "insertText"}));
	this.dispatchEvent(new Event("change", {bubbles: true}));
	return true;
}`

// findFields locates the sign-in form's fields in the main frame.
const findFields = `function(){
	for (const e of document.querySelectorAll("[data-conductor-field]")) e.removeAttribute("data-conductor-field");
	const vis = e => !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length);
	const inputs = Array.from(document.querySelectorAll("input")).filter(e => vis(e) && !e.disabled && !e.readOnly);
	const pw = inputs.find(e => (e.getAttribute("type") || "").toLowerCase() === "password");
	const typ = e => (e.getAttribute("type") || "text").toLowerCase();
	const attrs = e => ((e.name || "") + " " + (e.id || "") + " " + (e.getAttribute("autocomplete") || "") + " " + (e.getAttribute("inputmode") || "")).toLowerCase();
	const otp = inputs.find(e => typ(e) !== "password" && /one-time-code|otp|totp|2fa|mfa|verification|\bcode\b/.test(attrs(e)));
	let user = inputs.find(e => /username|email/.test((e.getAttribute("autocomplete") || "").toLowerCase()));
	if (!user && pw) {
		const before = inputs.filter(e => ["text", "email", "tel"].includes(typ(e)) && e !== otp && (e.compareDocumentPosition(pw) & Node.DOCUMENT_POSITION_FOLLOWING));
		user = before[before.length - 1];
	}
	const mark = (e, k) => { if (e) e.setAttribute("data-conductor-field", k); return !!e; };
	return {password: mark(pw, "password"), otp: mark(otp, "otp"), username: mark(user, "username")};
}`

// credentialFor resolves a credential the caller may use now: bound to its
// identity, and its provenance the operator's.
func (m *Manager) credentialFor(c Principal, st Settings, name string) (Credential, origin, error) {
	for _, cr := range st.Credentials {
		if cr.Name != name {
			continue
		}
		if !cr.For(c.Identity) {
			return Credential{}, origin{}, refuse("credential %q is not bound to this identity (%s)", name, c.Identity)
		}
		o, err := parseOrigin(cr.Origin)
		if err != nil {
			return Credential{}, origin{}, err
		}
		prov := m.Provenance
		if prov == nil {
			prov = OperatorOnly
		}
		if ok, why := prov(c, o.host); !ok {
			return Credential{}, origin{}, refuse("credential %q: %s", name, why)
		}
		return cr, o, nil
	}
	return Credential{}, origin{}, refuse("no credential %q is configured (browser.credentials)", name)
}

// secret fetches one value for one fill: fresh from the vault, never
// cached by conductor, never returned to the caller.
func (m *Manager) secret(ctx context.Context, ref *SecretRef) (string, error) {
	if m.Secrets == nil {
		return "", fmt.Errorf("browser: no vault is wired for credentials")
	}
	v, err := m.Secrets(ctx, ref.Vault, ref.Key)
	if err != nil {
		// The vault's error, never a value.
		return "", fmt.Errorf("browser: credential source %s: %w", ref.Vault, err)
	}
	if v == "" {
		return "", fmt.Errorf("browser: credential source %s returned nothing", ref.Vault)
	}
	return v, nil
}

// checkPage refuses a page that is not the credential's origin or not on
// the browser's allowlist, and returns its main frame.
func (m *Manager) checkPage(ctx context.Context, in *Instance, t *tab, o origin, allow []string) (frame, error) {
	fr, err := in.mainFrame(ctx, t)
	if err != nil {
		return frame{}, err
	}
	if err := o.check(fr.SecurityOrigin, fr.URL); err != nil {
		return frame{}, refuse("%v", err)
	}
	if err := checkURL(fr.URL, allow); err != nil {
		return frame{}, err
	}
	return fr, nil
}

// fillField fills one field of the main frame's document with a fresh value.
func (m *Manager) fillField(ctx context.Context, in *Instance, t *tab, fr frame, nodeID int64, cred Credential, field string) error {
	var ref *SecretRef
	switch field {
	case "username":
		ref = cred.Username
	case "password":
		ref = cred.Password
	case "otp":
		ref = cred.OTP
	default:
		return refuse("field is username, password or otp, not %q", field)
	}
	if ref == nil {
		return refuse("credential %q has no %s", cred.Name, field)
	}
	w, err := in.world(ctx, t, fr.ID)
	if err != nil {
		return err
	}
	obj, err := in.resolveIn(ctx, t, nodeID, w)
	if err != nil {
		return refuse("the field is not in the page's own document (an embedded frame, or the page changed)")
	}
	v, err := in.callOn(ctx, t, obj, fieldCheck, field)
	if err != nil {
		return err
	}
	if why := strings.Trim(string(v), `"`); why != "" {
		return refuse("cannot fill the %s there: %s", field, why)
	}
	val, err := m.secret(ctx, ref)
	if err != nil {
		return err
	}
	if field == "otp" {
		if val, err = OTPCode(val, time.Now()); err != nil {
			return err
		}
	}
	if field != "username" {
		in.remember(nodeID, val)
	}
	if _, err := in.callOn(ctx, t, obj, fieldSet, val); err != nil {
		return fmt.Errorf("browser: fill the %s: the page changed", field)
	}
	return nil
}

// markedNode finds the element findFields marked for a field.
func (in *Instance) markedNode(ctx context.Context, t *tab, ctxID int, field string) (int64, error) {
	var r struct {
		Result struct {
			ObjectID string `json:"objectId"`
		} `json:"result"`
	}
	if err := in.conn().Call(ctx, t.session, "Runtime.callFunctionOn", map[string]any{
		"functionDeclaration": `function(k){ return document.querySelector('[data-conductor-field="' + k + '"]'); }`,
		"executionContextId":  ctxID, "arguments": []map[string]any{{"value": field}},
	}, &r); err != nil {
		return 0, err
	}
	if r.Result.ObjectID == "" {
		return 0, fmt.Errorf("no %s field", field)
	}
	var d struct {
		Node struct {
			BackendNodeID int64 `json:"backendNodeId"`
		} `json:"node"`
	}
	if err := in.conn().Call(ctx, t.session, "DOM.describeNode", map[string]any{"objectId": r.Result.ObjectID}, &d); err != nil {
		return 0, err
	}
	return d.Node.BackendNodeID, nil
}

// submitFrom submits the form a field belongs to (Enter where there is no
// form).
func (in *Instance) submitFrom(ctx context.Context, t *tab, ctxID int, nodeID int64) error {
	obj, err := in.resolveIn(ctx, t, nodeID, ctxID)
	if err != nil {
		return err
	}
	v, err := in.callOn(ctx, t, obj, `function(){ if (this.form) { this.form.requestSubmit(); return true; } this.focus(); return false; }`)
	if err != nil {
		return err
	}
	if string(v) != "true" {
		if err := in.key(ctx, t, "Enter"); err != nil {
			return err
		}
	}
	in.settle(ctx, t, 15*time.Second)
	return nil
}

// login signs in with a credential: on its origin, it fills what the page
// asks for — username and password, then a one-time code — submits each,
// waits for an approval when the site asks for one, and uses the passkey
// when asked to (or when there is no password to give).
func (m *Manager) login(ctx context.Context, s *session, in *Instance, t *tab, c Principal, st Settings, cred Credential, o origin, allow []string, passkey bool, ref string, entry map[string]any) (map[string]any, error) {
	var steps []string
	fr, err := in.mainFrame(ctx, t)
	if err != nil {
		return nil, err
	}
	navigated := false
	if o.check(fr.SecurityOrigin, fr.URL) != nil && cred.LoginURL != "" {
		if err := checkURL(cred.LoginURL, allow); err != nil {
			return nil, err
		}
		if err := in.navigate(ctx, t, cred.LoginURL); err != nil {
			return nil, err
		}
		navigated = true
		steps = append(steps, "opened the sign-in page")
	}
	if fr, err = m.checkPage(ctx, in, t, o, allow); err != nil {
		return nil, err
	}
	if passkey {
		if err := m.usePasskey(ctx, in, t, cred, o, fr, ref); err != nil {
			return nil, err
		}
		steps = append(steps, "passkey")
		return map[string]any{"steps": steps}, nil
	}
	for round := 0; round < 3; round++ {
		if fr, err = m.checkPage(ctx, in, t, o, allow); err != nil {
			if len(steps) > 0 && errorsIsRefused(err) {
				// Signed in and moved on (to another site): done.
				break
			}
			return nil, err
		}
		w, err := in.world(ctx, t, fr.ID)
		if err != nil {
			return nil, err
		}
		raw, err := in.callIn(ctx, t, w, findFields)
		if err != nil {
			return nil, err
		}
		var found struct{ Password, OTP, Username bool }
		_ = json.Unmarshal(raw, &found)
		var submit int64
		if !found.Password && !(found.OTP && cred.OTP != nil) && !(found.Username && cred.Username != nil) && len(steps) == 0 && cred.LoginURL != "" && !navigated {
			// Nothing to fill here: start from the credential's sign-in page.
			if err := checkURL(cred.LoginURL, allow); err != nil {
				return nil, err
			}
			if err := in.navigate(ctx, t, cred.LoginURL); err != nil {
				return nil, err
			}
			navigated = true
			steps = append(steps, "opened the sign-in page")
			round--
			continue
		}
		switch {
		case found.Password && cred.Password != nil:
			if found.Username && cred.Username != nil {
				id, err := in.markedNode(ctx, t, w, "username")
				if err == nil {
					if err := m.fillField(ctx, in, t, fr, id, cred, "username"); err != nil {
						return nil, err
					}
					steps = append(steps, "username")
				}
			}
			id, err := in.markedNode(ctx, t, w, "password")
			if err != nil {
				return nil, err
			}
			if err := m.fillField(ctx, in, t, fr, id, cred, "password"); err != nil {
				return nil, err
			}
			steps, submit = append(steps, "password"), id
		case found.OTP && cred.OTP != nil:
			id, err := in.markedNode(ctx, t, w, "otp")
			if err != nil {
				return nil, err
			}
			if err := m.fillField(ctx, in, t, fr, id, cred, "otp"); err != nil {
				return nil, err
			}
			steps, submit = append(steps, "one-time code"), id
		case found.Username && cred.Username != nil && !found.Password && round == 0:
			// A username-first form.
			id, err := in.markedNode(ctx, t, w, "username")
			if err != nil {
				return nil, err
			}
			if err := m.fillField(ctx, in, t, fr, id, cred, "username"); err != nil {
				return nil, err
			}
			steps, submit = append(steps, "username"), id
		}
		if submit == 0 {
			break
		}
		w2, err := in.world(ctx, t, fr.ID)
		if err != nil {
			return nil, err
		}
		if err := in.submitFrom(ctx, t, w2, submit); err != nil {
			return nil, err
		}
		steps = append(steps, "submitted")
	}
	if len(steps) == 0 || (len(steps) == 1 && steps[0] == "opened the sign-in page") {
		hint := ""
		if cred.Passkey != nil {
			hint = " (for the passkey: login --passkey --ref <the site's passkey button>)"
		}
		return nil, fmt.Errorf("no sign-in field this credential can fill on this page — take a snapshot, or use fill-credential%s", hint)
	}
	out := map[string]any{"steps": steps}
	if cred.Approval {
		out["approval"] = m.awaitApproval(ctx, in, t, c, cred, 90*time.Second, entry)
	}
	return out, nil
}

func errorsIsRefused(err error) bool {
	return err != nil && strings.Contains(err.Error(), errRefused.Error())
}

// awaitApproval tells the operator a sign-in waits on their phone and waits
// for the page to move on (bounded): "approved", or "still waiting".
func (m *Manager) awaitApproval(ctx context.Context, in *Instance, t *tab, c Principal, cred Credential, d time.Duration, entry map[string]any) string {
	start, _ := in.pageInfo(ctx, t)
	if m.Notify != nil {
		m.Notify(fmt.Sprintf("approve the sign-in to %s (credential %s) for %s — on your phone", cred.Origin, cred.Name, c.Identity))
	}
	entry["notified"] = true
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "still waiting"
		case <-time.After(time.Second):
		}
		if u, _ := in.pageInfo(ctx, t); u != start {
			in.settle(ctx, t, 10*time.Second)
			return "approved (the page moved on)"
		}
	}
	return "still waiting — run conductor browser await-approval again"
}

// usePasskey signs in with the credential's passkey: a virtual
// authenticator holding only it, attached to this tab only while the page
// is on the bound origin, removed after.
func (m *Manager) usePasskey(ctx context.Context, in *Instance, t *tab, cred Credential, o origin, fr frame, ref string) error {
	if cred.Passkey == nil {
		return refuse("credential %q has no passkey", cred.Name)
	}
	raw, err := m.secret(ctx, cred.Passkey)
	if err != nil {
		return err
	}
	var pk Passkey
	if err := json.Unmarshal([]byte(raw), &pk); err != nil || pk.PrivateKey == "" || pk.CredentialID == "" {
		return fmt.Errorf("browser: credential %q: the stored passkey is not readable (register it: conductor browser passkey register %s)", cred.Name, cred.Name)
	}
	if !rpFits(pk.RPID, o.host) {
		return refuse("the passkey is for %s, not %s", pk.RPID, o.host)
	}
	if err := in.conn().Call(ctx, t.session, "WebAuthn.enable", map[string]any{"enableUI": false}, nil); err != nil {
		return err
	}
	var a struct {
		ID string `json:"authenticatorId"`
	}
	if err := in.conn().Call(ctx, t.session, "WebAuthn.addVirtualAuthenticator", map[string]any{"options": map[string]any{
		"protocol": "ctap2", "transport": "internal", "hasResidentKey": true,
		"hasUserVerification": true, "isUserVerified": true, "automaticPresenceSimulation": true,
	}}, &a); err != nil {
		return err
	}
	defer func() {
		c2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// The sign count moves on with each use: kept, so a site that checks
		// it keeps accepting the passkey.
		var got struct {
			Credentials []struct {
				CredentialID string `json:"credentialId"`
				SignCount    int    `json:"signCount"`
			} `json:"credentials"`
		}
		if in.conn().Call(c2, t.session, "WebAuthn.getCredentials", map[string]any{"authenticatorId": a.ID}, &got) == nil {
			for _, g := range got.Credentials {
				if g.CredentialID == pk.CredentialID && g.SignCount > pk.SignCount && m.PutSecret != nil {
					pk.SignCount = g.SignCount
					if b, err := json.Marshal(pk); err == nil {
						_ = m.PutSecret(c2, cred.Passkey.Vault, cred.Passkey.Key, string(b))
					}
				}
			}
		}
		_ = in.conn().Call(c2, t.session, "WebAuthn.removeVirtualAuthenticator", map[string]any{"authenticatorId": a.ID}, nil)
	}()
	if err := in.conn().Call(ctx, t.session, "WebAuthn.addCredential", map[string]any{"authenticatorId": a.ID, "credential": map[string]any{
		"credentialId": pk.CredentialID, "isResidentCredential": true, "rpId": pk.RPID,
		"privateKey": pk.PrivateKey, "userHandle": pk.UserHandle, "signCount": pk.SignCount,
	}}, nil); err != nil {
		return err
	}
	start, _ := in.pageInfo(ctx, t)
	if ref != "" {
		// The re-check: the click happens on the bound origin.
		if f2, err := in.mainFrame(ctx, t); err != nil || o.check(f2.SecurityOrigin, f2.URL) != nil || f2.ID != fr.ID {
			return refuse("the page left the credential's origin")
		}
		if err := in.click(ctx, t, ref); err != nil {
			return err
		}
	}
	for i := 0; i < 100; i++ {
		if u, _ := in.pageInfo(ctx, t); u != start {
			in.settle(ctx, t, 10*time.Second)
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("the page did not move on after the passkey was offered — click the site's passkey button (login --passkey --ref eN)")
}

// remember records a filled secret: its value is scrubbed from everything
// the agent gets back, its field masked in screenshots, and evaluate goes
// off in this browser.
func (in *Instance) remember(nodeID int64, v string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.secretNodes == nil {
		in.secretNodes = map[int64]bool{}
	}
	in.secretNodes[nodeID] = true
	if len(v) > 0 {
		in.secrets = append(in.secrets, v)
	}
}

// filled reports whether a credential was ever filled here.
func (in *Instance) filled() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return len(in.secretNodes) > 0
}

// secretValues are the values filled in this browser.
func (in *Instance) secretValues() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]string(nil), in.secrets...)
}

// Redacted is what a filled value reads as.
const Redacted = "[redacted]"

// scrub replaces every filled value in s.
func (in *Instance) scrub(s string) string {
	for _, v := range in.secretValues() {
		if v != "" {
			s = strings.ReplaceAll(s, v, Redacted)
		}
	}
	return s
}

// scrubAny scrubs every string in a reply.
func (in *Instance) scrubAny(v any) any {
	switch x := v.(type) {
	case string:
		return in.scrub(x)
	case map[string]any:
		for k, e := range x {
			x[k] = in.scrubAny(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = in.scrubAny(e)
		}
		return x
	case []string:
		for i, e := range x {
			x[i] = in.scrub(e)
		}
		return x
	case []TabInfo:
		for i := range x {
			x[i].Title, x[i].URL = in.scrub(x[i].Title), in.scrub(x[i].URL)
		}
		return x
	case []Download:
		for i := range x {
			x[i].Name, x[i].URL, x[i].Err = in.scrub(x[i].Name), in.scrub(x[i].URL), in.scrub(x[i].Err)
		}
		return x
	}
	return v
}

// secretRects are the boxes (CSS px, relative to the capture) of every
// field holding a filled value and every element whose own text shows one.
const secretRectsJS = `function(secrets, full){
	const out = [];
	const add = r => { if (r.width > 0 && r.height > 0) out.push([r.left + (full ? scrollX : 0), r.top + (full ? scrollY : 0), r.width, r.height]); };
	const has = s => typeof s === "string" && secrets.some(v => v && s.includes(v));
	for (const e of document.querySelectorAll("input, textarea")) if (has(e.value)) add(e.getBoundingClientRect());
	const w = document.createTreeWalker(document.body || document.documentElement, NodeFilter.SHOW_TEXT);
	for (let n = w.nextNode(); n; n = w.nextNode()) if (has(n.nodeValue) && n.parentElement) add(n.parentElement.getBoundingClientRect());
	return {rects: out, width: full ? document.documentElement.scrollWidth : innerWidth};
}`

// maskSecrets blacks out, in a screenshot, every field and text showing a
// filled value. A browser with filled values whose boxes cannot be found
// gives no screenshot at all.
func (in *Instance) maskSecrets(ctx context.Context, t *tab, shot []byte, full bool) ([]byte, error) {
	secrets := in.secretValues()
	if len(secrets) == 0 {
		return shot, nil
	}
	fail := func(err error) ([]byte, error) {
		return nil, fmt.Errorf("browser: a credential was filled in this browser and its fields could not be masked (%v) — no screenshot", err)
	}
	fr, err := in.mainFrame(ctx, t)
	if err != nil {
		return fail(err)
	}
	w, err := in.world(ctx, t, fr.ID)
	if err != nil {
		return fail(err)
	}
	raw, err := in.callIn(ctx, t, w, secretRectsJS, secrets, full)
	if err != nil {
		return fail(err)
	}
	var r struct {
		Rects [][4]float64 `json:"rects"`
		Width float64      `json:"width"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.Width <= 0 {
		return fail(fmt.Errorf("no page width"))
	}
	if len(r.Rects) == 0 {
		return shot, nil
	}
	return maskPNG(shot, r.Rects, r.Width)
}
