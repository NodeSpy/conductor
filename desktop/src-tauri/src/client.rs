//! HTTP client: cookie-jar session, SSH sign-in, and every call the app's
//! screens make (attention, approvals/decisions with SSH step-up,
//! conversations/chat, the bots roster). The event stream itself lives in
//! `stream.rs` (parsing) driven from `subscribe.rs`, which uses
//! `Client::event_stream_request` below to get a streaming response.
//!
//! One shared `Client` lives in `AppState` (see lib.rs); every Tauri command
//! that talks to the daemon goes through it, so there is exactly one signed-in
//! session, one cookie jar and one cached CSRF token for the whole app.

use std::sync::Arc;

use reqwest::{Client as HttpClient, StatusCode};
use serde::{Deserialize, Serialize};
use tokio::sync::RwLock;

use crate::sign;
use crate::types::AttentionSnapshot;

#[derive(Debug, Deserialize)]
struct SshBeginResponse {
    challenge_id: String,
    challenge: String,
    namespace: String,
    #[allow(dead_code)]
    login_code: String,
    #[allow(dead_code)]
    expires: String,
}

#[derive(Debug, Deserialize)]
struct SshFinishResponse {
    #[allow(dead_code)]
    operator: String,
    csrf: String,
    #[allow(dead_code)]
    method: String,
}

/// A step-up finish only ever returns a token (no CSRF rotation: the session
/// cookie's secret rotates, but `sess.CSRF` is unchanged — see client.rs's
/// `decide`, which keeps using the cached CSRF token for the decision POST).
#[derive(Debug, Deserialize)]
struct StepupFinishResponse {
    token: String,
}

/// The daemon's own error shape, `{"error": {"code","message","hint?"}}`,
/// used by every dashboard HTTP API response (internal/dashboard/*). Decoded
/// here so callers (and, through `ClientError::Daemon`'s Display, the Tauri
/// command error strings the frontend parses in src/api/client.ts) can act on
/// `code` — e.g. `bots_not_configured`.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct DaemonError {
    pub code: String,
    pub message: String,
    #[serde(default)]
    pub hint: Option<String>,
}

#[derive(Debug, Deserialize)]
struct DaemonErrorBody {
    error: DaemonError,
}

#[derive(Debug, thiserror::Error)]
pub enum ClientError {
    #[error("network error: {0}")]
    Network(#[from] reqwest::Error),
    #[allow(dead_code)] // reserved for a future structured sign-in error path
    #[error("sign-in: {0}")]
    SignIn(String),
    #[error("signing failed: {0}")]
    Sign(#[from] sign::SignError),
    #[error("unauthorized")]
    Unauthorized,
    #[error("{}", .0.message)]
    Daemon(DaemonError),
    #[error("unexpected status {0}: {1}")]
    Status(StatusCode, String),
}

impl ClientError {
    /// JSON-encodes a `Daemon` error so the Tauri command boundary (which
    /// only carries `Result<T, String>`) can round-trip `code`/`hint` to the
    /// frontend; anything else is just its plain message (src/api/client.ts
    /// tries to parse the error string as this shape and falls back to the
    /// raw text).
    pub fn to_command_error(&self) -> String {
        if let ClientError::Daemon(e) = self {
            if let Ok(s) = serde_json::to_string(e) {
                return s;
            }
        }
        self.to_string()
    }
}

/// The approval view shared by a bot proposal (`bot`+`proposal`, purpose
/// "approve") and a browser purchase (`purchase`, purpose
/// "browser.purchase"). `summary` is raw, loosely-shaped JSON from the
/// daemon. The decision's digest always comes from `digests` here — it is
/// never computed client-side.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct ApprovalView {
    #[serde(default)]
    pub bot: Option<String>,
    #[serde(default)]
    pub proposal: Option<String>,
    #[serde(default)]
    pub purchase: Option<String>,
    pub action_sha256: String,
    pub digests: ApprovalDigests,
    pub descriptions: ApprovalDescriptions,
    #[serde(default)]
    pub summary: serde_json::Value,
    #[serde(default)]
    pub operators: Vec<String>,
    pub purpose: String,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct ApprovalDigests {
    pub approve: String,
    pub deny: String,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct ApprovalDescriptions {
    pub approve: String,
    pub deny: String,
}

/// "proposal" (a bot's approval) or "purchase" (a browser purchase): the two
/// kinds of thing Needs you can ask a decision on. Each maps to a different
/// pair of daemon routes.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ApprovalKind {
    Proposal,
    Purchase,
}

impl ApprovalKind {
    pub fn parse(s: &str) -> Result<Self, String> {
        match s {
            "proposal" => Ok(Self::Proposal),
            "purchase" => Ok(Self::Purchase),
            other => Err(format!("unknown approval kind {other:?}")),
        }
    }
}

/// A signed-in session against one daemon. Cheap to clone (shares the
/// underlying reqwest client, which owns the cookie jar, and the csrf cell).
#[derive(Clone)]
pub struct Client {
    http: HttpClient,
    base_url: String,
    pubkey_path: String,
    csrf: Arc<RwLock<Option<String>>>,
}

impl Client {
    pub fn new(base_url: &str, pubkey_path: &str) -> Result<Self, ClientError> {
        let http = HttpClient::builder()
            .cookie_store(true)
            .user_agent("conductor-desktop/0.1")
            .build()?;
        Ok(Self {
            http,
            base_url: base_url.trim_end_matches('/').to_string(),
            pubkey_path: pubkey_path.to_string(),
            csrf: Arc::new(RwLock::new(None)),
        })
    }

    /// Kept for tests and future UI (e.g. showing the connected daemon).
    #[allow(dead_code)]
    pub fn base_url(&self) -> &str {
        &self.base_url
    }

    fn url(&self, path: &str) -> String {
        format!("{}{}", self.base_url, path)
    }

    pub async fn csrf(&self) -> Option<String> {
        self.csrf.read().await.clone()
    }

    /// Full sign-in: begin, sign (no confirmation dialog — this is the app's
    /// own background sign-in, not a page-initiated one), finish. On success
    /// the session cookie is in the jar and the csrf token is cached.
    pub async fn sign_in(&self, operator: &str) -> Result<(), ClientError> {
        let begin: SshBeginResponse = self
            .post_json_noorigin(
                &self.url("/api/v1/auth/ssh/begin"),
                &serde_json::json!({ "operator": operator }),
            )
            .await?;

        tracing::info!(
            challenge_id = %begin.challenge_id,
            namespace = %begin.namespace,
            "signing in: signing challenge with configured key"
        );
        let signature = sign::ssh_sign(&self.pubkey_path, &begin.namespace, &begin.challenge).await?;

        let finish: SshFinishResponse = self
            .post_json_noorigin(
                &self.url("/api/v1/auth/ssh/finish"),
                &serde_json::json!({
                    "challenge_id": begin.challenge_id,
                    "signature": signature,
                }),
            )
            .await?;

        *self.csrf.write().await = Some(finish.csrf);
        Ok(())
    }

    /// POSTs JSON and decodes a JSON response, explicitly never sending an
    /// Origin header (reqwest does not add one for non-browser requests, so
    /// this is the default — stated here so it stays true on purpose). Used
    /// only for the unauthenticated sign-in calls; everything past sign-in
    /// goes through `get_json`/`send_json`, which attach the CSRF header.
    async fn post_json_noorigin<T: serde::de::DeserializeOwned>(
        &self,
        url: &str,
        body: &serde_json::Value,
    ) -> Result<T, ClientError> {
        let resp = self.http.post(url).json(body).send().await?;
        Self::decode(resp).await
    }

    async fn decode<T: serde::de::DeserializeOwned>(resp: reqwest::Response) -> Result<T, ClientError> {
        let status = resp.status();
        if status == StatusCode::UNAUTHORIZED {
            return Err(ClientError::Unauthorized);
        }
        if !status.is_success() {
            let text = resp.text().await.unwrap_or_default();
            if let Ok(body) = serde_json::from_str::<DaemonErrorBody>(&text) {
                return Err(ClientError::Daemon(body.error));
            }
            return Err(ClientError::Status(status, text));
        }
        Ok(resp.json::<T>().await?)
    }

    /// A plain GET, decoded as JSON. Every read-only endpoint the app's
    /// screens use (attention, approval views, conversations, messages,
    /// bots) goes through this one method.
    pub async fn get_json<T: serde::de::DeserializeOwned>(&self, path: &str) -> Result<T, ClientError> {
        let resp = self.http.get(self.url(path)).send().await?;
        Self::decode(resp).await
    }

    /// A POST with a JSON body, the cached CSRF token attached (every
    /// non-GET dashboard API call requires `X-Conductor-CSRF`).
    pub async fn post_json<T: serde::de::DeserializeOwned>(&self, path: &str, body: &serde_json::Value) -> Result<T, ClientError> {
        let csrf = self.csrf().await.unwrap_or_default();
        let resp = self
            .http
            .post(self.url(path))
            .header("X-Conductor-CSRF", csrf)
            .json(body)
            .send()
            .await?;
        Self::decode(resp).await
    }

    /// Like `post_json`, plus the step-up token header a decision needs.
    async fn post_json_stepup<T: serde::de::DeserializeOwned>(
        &self,
        path: &str,
        body: &serde_json::Value,
        stepup_token: &str,
    ) -> Result<T, ClientError> {
        let csrf = self.csrf().await.unwrap_or_default();
        let resp = self
            .http
            .post(self.url(path))
            .header("X-Conductor-CSRF", csrf)
            .header("X-Conductor-Stepup", stepup_token)
            .json(body)
            .send()
            .await?;
        Self::decode(resp).await
    }

    /// A form-encoded POST (the daemon reads `/conversations/{id}/read` with
    /// `r.ParseForm()`, not a JSON body).
    pub async fn post_form<T: serde::de::DeserializeOwned>(&self, path: &str, form: &[(&str, &str)]) -> Result<T, ClientError> {
        let csrf = self.csrf().await.unwrap_or_default();
        let resp = self
            .http
            .post(self.url(path))
            .header("X-Conductor-CSRF", csrf)
            .form(form)
            .send()
            .await?;
        Self::decode(resp).await
    }

    pub async fn attention_snapshot(&self) -> Result<AttentionSnapshot, ClientError> {
        self.get_json("/api/v1/attention").await
    }

    /// A raised item's `source` is empty and it is dismissable this way.
    pub async fn dismiss(&self, key: &str) -> Result<(), ClientError> {
        let _: serde_json::Value = self.post_json(&format!("/api/v1/attention/{key}/dismiss"), &serde_json::json!({})).await?;
        Ok(())
    }

    fn approval_path(kind: ApprovalKind, bot: Option<&str>, id: &str) -> Result<String, ClientError> {
        match kind {
            ApprovalKind::Proposal => {
                let bot = bot.ok_or_else(|| ClientError::Status(StatusCode::BAD_REQUEST, "a proposal needs a bot".into()))?;
                Ok(format!("/api/v1/bots/{bot}/proposals/{id}"))
            }
            ApprovalKind::Purchase => Ok(format!("/api/v1/browser/purchases/{id}")),
        }
    }

    pub async fn approval_view(&self, kind: ApprovalKind, bot: Option<&str>, id: &str) -> Result<ApprovalView, ClientError> {
        let path = Self::approval_path(kind, bot, id)?;
        self.get_json(&format!("{path}/approval")).await
    }

    /// Decides a proposal or purchase: fetches the approval view (again, in
    /// this same session — the daemon's step-up "describes" the action from
    /// a GET first), begins an SSH step-up for the decision's digest, signs
    /// it with the configured key (the in-app Approve/Deny button is the
    /// confirmation — no extra dialog, unlike a page-initiated `ssh_sign`),
    /// finishes the step-up, then POSTs the decision with the resulting
    /// token in `X-Conductor-Stepup`.
    pub async fn decide(
        &self,
        kind: ApprovalKind,
        bot: Option<&str>,
        id: &str,
        decision: &str,
        reason: Option<&str>,
    ) -> Result<(), ClientError> {
        let path = Self::approval_path(kind, bot, id)?;
        let view = self.approval_view(kind, bot, id).await?;
        let digest = match decision {
            "approve" => &view.digests.approve,
            "deny" => &view.digests.deny,
            other => return Err(ClientError::Status(StatusCode::BAD_REQUEST, format!("unknown decision {other:?}"))),
        };

        let begin: SshBeginResponse = self
            .post_json(
                "/api/v1/auth/stepup/ssh/begin",
                &serde_json::json!({ "purpose": view.purpose, "digest": digest }),
            )
            .await?;
        let signature = sign::ssh_sign(&self.pubkey_path, &begin.namespace, &begin.challenge).await?;
        let finish: StepupFinishResponse = self
            .post_json(
                "/api/v1/auth/stepup/ssh/finish",
                &serde_json::json!({ "challenge_id": begin.challenge_id, "signature": signature }),
            )
            .await?;

        let mut body = serde_json::json!({ "decision": decision });
        if let Some(r) = reason.filter(|r| !r.is_empty()) {
            body["reason"] = serde_json::Value::String(r.to_string());
        }
        let _: serde_json::Value = self.post_json_stepup(&format!("{path}/decision"), &body, &finish.token).await?;
        Ok(())
    }

    pub async fn conversations(&self) -> Result<serde_json::Value, ClientError> {
        self.get_json("/api/v1/conversations").await
    }

    pub async fn messages(&self, id: &str, after: u64, mark: bool) -> Result<serde_json::Value, ClientError> {
        let mark_qs = if mark { "&mark=1" } else { "" };
        self.get_json(&format!("/api/v1/conversations/{id}/messages?after={after}{mark_qs}")).await
    }

    pub async fn send_message(&self, id: &str, text: &str, to: Option<&[String]>, everyone: bool) -> Result<serde_json::Value, ClientError> {
        let mut body = serde_json::json!({ "text": text, "everyone": everyone });
        if let Some(to) = to {
            body["to"] = serde_json::json!(to);
        }
        self.post_json(&format!("/api/v1/conversations/{id}/messages"), &body).await
    }

    /// Form-encoded, per the daemon's handler (`r.ParseForm`), not JSON.
    pub async fn mark_read(&self, id: &str, upto: u64) -> Result<(), ClientError> {
        let upto = upto.to_string();
        let _: serde_json::Value = self.post_form(&format!("/api/v1/conversations/{id}/read"), &[("upto", upto.as_str())]).await?;
        Ok(())
    }

    pub async fn bots(&self) -> Result<serde_json::Value, ClientError> {
        self.get_json("/api/v1/bots").await
    }

    pub async fn bot(&self, name: &str) -> Result<serde_json::Value, ClientError> {
        self.get_json(&format!("/api/v1/bots/{name}")).await
    }

    pub async fn bot_proposals(&self, name: &str) -> Result<serde_json::Value, ClientError> {
        self.get_json(&format!("/api/v1/bots/{name}/proposals")).await
    }

    /// Opens the raw streaming GET for `/api/v1/events`; `stream.rs`'s caller
    /// chunks the body and feeds it to `SseParser`.
    pub async fn event_stream_request(
        &self,
        topics: &str,
        cursor: Option<&str>,
    ) -> Result<reqwest::Response, ClientError> {
        let mut req = self
            .http
            .get(self.url(&format!("/api/v1/events?topics={topics}")))
            .header("Accept", "text/event-stream");
        if let Some(c) = cursor {
            req = req.header("Last-Event-ID", c);
        }
        let resp = req.send().await?;
        let status = resp.status();
        if status == StatusCode::UNAUTHORIZED {
            return Err(ClientError::Unauthorized);
        }
        if !status.is_success() {
            let text = resp.text().await.unwrap_or_default();
            return Err(ClientError::Status(status, text));
        }
        Ok(resp)
    }
}

impl std::fmt::Debug for Client {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Client").field("base_url", &self.base_url).finish()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command as StdCommand;
    use tiny_http::{Header, Response, Server};

    /// A throwaway ed25519 keypair for tests that need a real
    /// `ssh-keygen -Y sign` round-trip (the decide flow signs a step-up
    /// challenge). `ssh-keygen -Y sign -f <path>` uses the key at `<path>`
    /// directly when it's a private key file — no ssh-agent needed — which
    /// is exactly what these tests rely on (see sign.rs's doc comment).
    struct TestKey {
        _dir: tempfile::TempDir,
        path: String,
    }

    fn test_key() -> TestKey {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("id_ed25519");
        let status = StdCommand::new("ssh-keygen")
            .args(["-q", "-t", "ed25519", "-N", "", "-f"])
            .arg(&path)
            .status()
            .expect("ssh-keygen must be on PATH for this test");
        assert!(status.success());
        TestKey {
            path: path.to_string_lossy().into_owned(),
            _dir: dir,
        }
    }

    fn body_json(req: &mut tiny_http::Request) -> serde_json::Value {
        let mut s = String::new();
        req.as_reader().read_to_string(&mut s).unwrap();
        serde_json::from_str(&s).unwrap_or(serde_json::Value::Null)
    }

    fn body_form(req: &mut tiny_http::Request) -> std::collections::HashMap<String, String> {
        let mut s = String::new();
        req.as_reader().read_to_string(&mut s).unwrap();
        url_form_decode(&s)
    }

    /// Tiny `application/x-www-form-urlencoded` decoder (percent-decoding
    /// only the handful of escapes these tests produce) — enough to assert
    /// `mark_read`'s form body without adding a parsing crate.
    fn url_form_decode(s: &str) -> std::collections::HashMap<String, String> {
        s.split('&')
            .filter(|p| !p.is_empty())
            .map(|pair| {
                let mut it = pair.splitn(2, '=');
                let k = it.next().unwrap_or("").to_string();
                let v = it.next().unwrap_or("").replace('+', " ");
                (k, v)
            })
            .collect()
    }

    fn json_response(code: u32, body: serde_json::Value) -> Response<std::io::Cursor<Vec<u8>>> {
        let data = serde_json::to_vec(&body).unwrap();
        Response::from_data(data)
            .with_status_code(code)
            .with_header(Header::from_bytes(&b"Content-Type"[..], &b"application/json"[..]).unwrap())
    }

    #[test]
    fn attention_snapshot_decodes() {
        let server = Server::http("127.0.0.1:0").unwrap();
        let addr = server.server_addr().to_ip().unwrap();
        std::thread::spawn(move || {
            let req = server.recv().unwrap();
            assert_eq!(req.url(), "/api/v1/attention");
            req.respond(json_response(
                200,
                serde_json::json!({
                    "items": [{"key":"k1","kind":"x","title":"t","opened":"2020-01-01T00:00:00Z","severity":"warn"}],
                    "cursor": "c1"
                }),
            ))
            .unwrap();
        });
        let rt = tokio::runtime::Runtime::new().unwrap();
        rt.block_on(async {
            let client = Client::new(&format!("http://{addr}"), "/nonexistent.pub").unwrap();
            let snap = client.attention_snapshot().await.unwrap();
            assert_eq!(snap.cursor, "c1");
            assert_eq!(snap.items.len(), 1);
            assert_eq!(snap.items[0].key, "k1");
        });
    }

    #[test]
    fn dismiss_sends_csrf_header() {
        let server = Server::http("127.0.0.1:0").unwrap();
        let addr = server.server_addr().to_ip().unwrap();
        std::thread::spawn(move || {
            let req = server.recv().unwrap();
            assert_eq!(req.url(), "/api/v1/attention/k1/dismiss");
            let csrf = req
                .headers()
                .iter()
                .find(|h| h.field.as_str().as_str().eq_ignore_ascii_case("X-Conductor-CSRF"))
                .map(|h| h.value.as_str().to_string());
            assert_eq!(csrf.as_deref(), Some("tok-123"));
            req.respond(json_response(200, serde_json::json!({}))).unwrap();
        });
        let rt = tokio::runtime::Runtime::new().unwrap();
        rt.block_on(async {
            let client = Client::new(&format!("http://{addr}"), "/nonexistent.pub").unwrap();
            *client.csrf.write().await = Some("tok-123".to_string());
            client.dismiss("k1").await.unwrap();
        });
    }

    #[test]
    fn mark_read_sends_form_body_not_json() {
        let server = Server::http("127.0.0.1:0").unwrap();
        let addr = server.server_addr().to_ip().unwrap();
        std::thread::spawn(move || {
            let mut req = server.recv().unwrap();
            assert_eq!(req.url(), "/api/v1/conversations/bot:a/read");
            let ct = req
                .headers()
                .iter()
                .find(|h| h.field.as_str().as_str().eq_ignore_ascii_case("Content-Type"))
                .map(|h| h.value.as_str().to_string())
                .unwrap_or_default();
            assert!(ct.contains("x-www-form-urlencoded"), "got content-type {ct:?}");
            let form = body_form(&mut req);
            assert_eq!(form.get("upto").map(String::as_str), Some("42"));
            req.respond(json_response(200, serde_json::json!({"read": true}))).unwrap();
        });
        let rt = tokio::runtime::Runtime::new().unwrap();
        rt.block_on(async {
            let client = Client::new(&format!("http://{addr}"), "/nonexistent.pub").unwrap();
            client.mark_read("bot:a", 42).await.unwrap();
        });
    }

    #[test]
    fn get_json_returns_daemon_error_on_error_body() {
        let server = Server::http("127.0.0.1:0").unwrap();
        let addr = server.server_addr().to_ip().unwrap();
        std::thread::spawn(move || {
            let req = server.recv().unwrap();
            req.respond(json_response(
                404,
                serde_json::json!({"error": {"code": "bots_not_configured", "message": "No bots are configured on this daemon."}}),
            ))
            .unwrap();
        });
        let rt = tokio::runtime::Runtime::new().unwrap();
        rt.block_on(async {
            let client = Client::new(&format!("http://{addr}"), "/nonexistent.pub").unwrap();
            let err = client.bots().await.unwrap_err();
            match err {
                ClientError::Daemon(d) => assert_eq!(d.code, "bots_not_configured"),
                other => panic!("expected a Daemon error, got {other:?}"),
            }
        });
    }

    /// The full decide flow: GET the approval view (for the digest), begin
    /// an SSH step-up for it, sign the challenge with the configured key,
    /// finish the step-up, then POST the decision with the resulting token
    /// in `X-Conductor-Stepup` — and the cached CSRF token on every POST.
    #[test]
    fn decide_does_stepup_then_posts_decision_with_header() {
        let key = test_key();
        let server = Server::http("127.0.0.1:0").unwrap();
        let addr = server.server_addr().to_ip().unwrap();
        std::thread::spawn(move || {
            let req = server.recv().unwrap();
            assert_eq!(req.url(), "/api/v1/bots/platform-sre/proposals/p1/approval");
            req.respond(json_response(
                200,
                serde_json::json!({
                    "bot": "platform-sre",
                    "proposal": "p1",
                    "action_sha256": "sha256:abc",
                    "digests": {"approve": "sha256:approve-digest", "deny": "sha256:deny-digest"},
                    "descriptions": {"approve": "Restart the pods", "deny": "Don't restart"},
                    "summary": "elevated 5xx",
                    "operators": ["daniel"],
                    "purpose": "approve"
                }),
            ))
            .unwrap();

            let mut req = server.recv().unwrap();
            assert_eq!(req.url(), "/api/v1/auth/stepup/ssh/begin");
            let csrf = req
                .headers()
                .iter()
                .find(|h| h.field.as_str().as_str().eq_ignore_ascii_case("X-Conductor-CSRF"))
                .map(|h| h.value.as_str().to_string());
            assert_eq!(csrf.as_deref(), Some("csrf-1"));
            let begin_body = body_json(&mut req);
            assert_eq!(begin_body["purpose"], "approve");
            assert_eq!(begin_body["digest"], "sha256:approve-digest");
            req.respond(json_response(
                200,
                serde_json::json!({
                    "challenge_id": "ch1",
                    "challenge": "conductor-dashboard-stepup v1 digest=sha256:approve-digest",
                    "namespace": sign::STEPUP_NAMESPACE,
                    "login_code": "AB12",
                    "expires": "2099-01-01T00:00:00Z"
                }),
            ))
            .unwrap();

            let mut req = server.recv().unwrap();
            assert_eq!(req.url(), "/api/v1/auth/stepup/ssh/finish");
            let finish_body = body_json(&mut req);
            assert_eq!(finish_body["challenge_id"], "ch1");
            assert!(finish_body["signature"].as_str().unwrap().contains("BEGIN SSH SIGNATURE"));
            req.respond(json_response(200, serde_json::json!({"token": "stepup-tok"}))).unwrap();

            let mut req = server.recv().unwrap();
            assert_eq!(req.url(), "/api/v1/bots/platform-sre/proposals/p1/decision");
            let stepup = req
                .headers()
                .iter()
                .find(|h| h.field.as_str().as_str().eq_ignore_ascii_case("X-Conductor-Stepup"))
                .map(|h| h.value.as_str().to_string());
            assert_eq!(stepup.as_deref(), Some("stepup-tok"));
            let csrf = req
                .headers()
                .iter()
                .find(|h| h.field.as_str().as_str().eq_ignore_ascii_case("X-Conductor-CSRF"))
                .map(|h| h.value.as_str().to_string());
            assert_eq!(csrf.as_deref(), Some("csrf-1"));
            let decision_body = body_json(&mut req);
            assert_eq!(decision_body["decision"], "approve");
            req.respond(json_response(
                200,
                serde_json::json!({"bot": "platform-sre", "proposal": "p1", "decision": "approve", "by": "daniel"}),
            ))
            .unwrap();
        });

        let rt = tokio::runtime::Runtime::new().unwrap();
        rt.block_on(async {
            let client = Client::new(&format!("http://{addr}"), &key.path).unwrap();
            *client.csrf.write().await = Some("csrf-1".to_string());
            client
                .decide(ApprovalKind::Proposal, Some("platform-sre"), "p1", "approve", None)
                .await
                .unwrap();
        });
    }

    /// A session that has expired mid-use (401) is retried exactly once by
    /// `lib.rs`'s `with_client` — this test covers the client-level half:
    /// a 401 surfaces as `ClientError::Unauthorized`, distinct from a
    /// `Daemon` error, so the caller knows to re-sign-in and retry.
    #[test]
    fn unauthorized_status_maps_to_unauthorized_error() {
        let server = Server::http("127.0.0.1:0").unwrap();
        let addr = server.server_addr().to_ip().unwrap();
        std::thread::spawn(move || {
            let req = server.recv().unwrap();
            req.respond(Response::from_string("nope").with_status_code(401)).unwrap();
        });
        let rt = tokio::runtime::Runtime::new().unwrap();
        rt.block_on(async {
            let client = Client::new(&format!("http://{addr}"), "/nonexistent.pub").unwrap();
            let err = client.attention_snapshot().await.unwrap_err();
            assert!(matches!(err, ClientError::Unauthorized));
        });
    }
}
