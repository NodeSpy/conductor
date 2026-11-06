//! Wire types shared by the snapshot (`/api/v1/attention`) and the event
//! stream (`/api/v1/events`). Kept intentionally permissive (`serde(default)`
//! / `Option`) since core may add fields the client doesn't act on yet.

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Item {
    pub key: String,
    pub kind: String,
    pub title: String,
    #[serde(default)]
    pub detail: Option<String>,
    #[serde(default)]
    pub link: Option<String>,
    #[serde(default)]
    pub subject: Option<String>,
    pub opened: String,
    pub severity: String,
    /// Empty/absent: a *raised* item the operator may dismiss
    /// (`POST /api/v1/attention/{key}/dismiss`). Non-empty: a *sourced* item
    /// that closes on its own when its source stops reporting it open.
    #[serde(default)]
    pub source: Option<String>,
}

impl Item {
    /// See `Client::dismiss`: not wired into the tray UI yet.
    #[allow(dead_code)]
    pub fn is_dismissable(&self) -> bool {
        self.source.as_deref().unwrap_or("").is_empty()
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Default)]
pub struct AttentionSnapshot {
    #[serde(default)]
    pub items: Vec<Item>,
    pub cursor: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ChatMessage {
    pub seq: Option<u64>,
    pub at: Option<String>,
    pub from: String,
    pub text: String,
    #[serde(flatten)]
    pub extra: serde_json::Map<String, serde_json::Value>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ChatEnvelope {
    pub conversation: String,
    pub message: ChatMessage,
}

impl ChatMessage {
    pub fn from_bot(&self) -> bool {
        self.from.starts_with("bot:")
    }

    pub fn from_operator(&self, operator: &str) -> bool {
        self.from == format!("operator:{operator}")
    }
}

/// One parsed `data:` payload off the attention/chat SSE stream (not the
/// `reset` event, which `stream.rs` handles separately since its shape
/// differs: `{"type":"reset","topic":"reset","id":"<cursor>"}`, no `seq`/`ts`).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EventEnvelope {
    pub id: String,
    pub seq: u64,
    pub topic: String,
    #[serde(rename = "type")]
    pub kind: String,
    pub ts: String,
    pub data: serde_json::Value,
}

#[derive(Debug, Clone, PartialEq)]
pub enum StreamEvent {
    Attention { envelope: EventEnvelope, item: Item },
    Chat { envelope: EventEnvelope, chat: ChatEnvelope },
    Reset { cursor: String },
    Other(EventEnvelope),
}
