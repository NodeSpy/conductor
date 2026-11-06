//! HTTP client: cookie-jar session, SSH sign-in, and the snapshot/dismiss
//! calls. The event stream itself lives in `stream.rs` (parsing) driven from
//! `lib.rs` (the subscription loop), which uses `Client::event_stream_request`
//! below to get a streaming response.

use std::sync::Arc;

use reqwest::{Client as HttpClient, StatusCode};
use serde::Deserialize;
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
    #[error("unexpected status {0}: {1}")]
    Status(StatusCode, String),
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
    /// this is the default — stated here so it stays true on purpose).
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
            return Err(ClientError::Status(status, text));
        }
        Ok(resp.json::<T>().await?)
    }

    pub async fn attention_snapshot(&self) -> Result<AttentionSnapshot, ClientError> {
        let resp = self
            .http
            .get(self.url("/api/v1/attention"))
            .send()
            .await?;
        Self::decode(resp).await
    }

    /// Not wired into the tray yet (no "Dismiss" menu item per the design),
    /// but part of the documented API: a raised item's `source` is empty and
    /// it is dismissable this way.
    #[allow(dead_code)]
    pub async fn dismiss(&self, key: &str) -> Result<(), ClientError> {
        let csrf = self.csrf().await.unwrap_or_default();
        let resp = self
            .http
            .post(self.url(&format!("/api/v1/attention/{key}/dismiss")))
            .header("X-Conductor-CSRF", csrf)
            .send()
            .await?;
        let status = resp.status();
        if status == StatusCode::UNAUTHORIZED {
            return Err(ClientError::Unauthorized);
        }
        if !status.is_success() {
            let text = resp.text().await.unwrap_or_default();
            return Err(ClientError::Status(status, text));
        }
        Ok(())
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
