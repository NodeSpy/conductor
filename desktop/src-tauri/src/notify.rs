//! Decides what to notify about. Pure/testable: it takes a batch of stream
//! events and the current "am I catching up after a gap" flag and returns a
//! plan (a list of `Notification`s to actually fire). The actual firing
//! (tauri-plugin-notification) lives in `lib.rs`, which is a thin adapter over
//! this.

use std::time::{Duration, Instant};

use crate::types::{ChatEnvelope, Item};

pub const COALESCE_THRESHOLD: usize = 3;

/// Debounces a run of newly-opened items into one batch: a reconnect/wake
/// catch-up typically delivers several in quick succession, and we want to
/// judge "was this a burst" (`plan_attention`'s coalesce threshold) only once
/// the arrivals have actually paused, not after every single item. A live
/// trickle (one item, nothing else for a while) still flushes — just after
/// one debounce interval — so it isn't held back.
#[derive(Debug, Default)]
pub struct BurstCollector {
    items: Vec<Item>,
    deadline: Option<Instant>,
}

impl BurstCollector {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn push(&mut self, item: Item, now: Instant, debounce: Duration) {
        self.items.push(item);
        self.deadline = Some(now + debounce);
    }

    pub fn is_empty(&self) -> bool {
        self.items.is_empty()
    }

    /// The instant `push` would make this ready to flush, if anything is
    /// buffered — for the caller's `tokio::time::sleep_until`.
    pub fn deadline(&self) -> Option<Instant> {
        self.deadline
    }

    pub fn ready(&self, now: Instant) -> bool {
        self.deadline.is_some_and(|d| now >= d)
    }

    /// Drains the buffer. The caller is expected to call this only once
    /// `ready` is true (or on an unconditional flush, e.g. shutdown).
    pub fn take(&mut self) -> Vec<Item> {
        self.deadline = None;
        std::mem::take(&mut self.items)
    }
}

#[derive(Debug, Clone, PartialEq)]
pub struct Notification {
    pub title: String,
    pub body: String,
    /// Where to navigate if the user clicks it (a `#/...` hash route,
    /// resolved against the daemon origin). `None` for a coalesced
    /// "N things need you" with no single destination.
    pub link: Option<String>,
}

#[derive(Debug, Default)]
pub struct NotificationPlanner {
    /// Items currently open, by key — drives the tray count. Also lets us
    /// recognize "updated" vs "opened" (an update to an already-open item
    /// never notifies).
    open: std::collections::HashMap<String, Item>,
}

/// What happened to one attention item in a batch, independent of why
/// (`opened`/`resolved`/`updated` as sent by the server).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AttentionChange {
    Opened,
    Resolved,
    Updated,
}

impl NotificationPlanner {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn open_count(&self) -> usize {
        self.open.len()
    }

    pub fn load_snapshot(&mut self, items: Vec<Item>) {
        self.open = items.into_iter().map(|i| (i.key.clone(), i)).collect();
    }

    /// Applies one attention change to in-memory state. Returns the item
    /// if this is a newly-opened item worth notifying about on its own (the
    /// caller decides whether to actually fire it, or fold it into a
    /// coalesced batch, based on how many such items arrived together).
    pub fn apply_attention(&mut self, change: AttentionChange, item: Item) -> bool {
        match change {
            AttentionChange::Opened => {
                let is_new = !self.open.contains_key(&item.key);
                self.open.insert(item.key.clone(), item);
                is_new
            }
            AttentionChange::Resolved => {
                self.open.remove(&item.key);
                false
            }
            AttentionChange::Updated => {
                self.open.insert(item.key.clone(), item);
                false
            }
        }
    }

    /// Builds the notification plan for a batch of newly-opened items
    /// (already filtered to real `Opened` changes via `apply_attention`).
    /// More than `COALESCE_THRESHOLD` collapses to one summary notification —
    /// meant for a catch-up burst after reconnect/wake, not normal live
    /// trickle (the caller decides batch boundaries).
    pub fn plan_attention(&self, newly_opened: &[Item]) -> Vec<Notification> {
        if newly_opened.is_empty() {
            return vec![];
        }
        if newly_opened.len() > COALESCE_THRESHOLD {
            return vec![Notification {
                title: format!("{} things need you", newly_opened.len()),
                body: String::new(),
                link: None,
            }];
        }
        newly_opened
            .iter()
            .map(|item| Notification {
                title: item.title.clone(),
                body: item.detail.clone().unwrap_or_default(),
                // In-app route, not the item's own daemon-dashboard link:
                // notifications open #/needs/<key>, which the app's Needs
                // screen resolves to the right approval or action itself.
                link: Some(format!("#/needs/{}", item.key)),
            })
            .collect()
    }

    /// Chat notification for one message, or `None` if it shouldn't notify
    /// (operator's own message, or not from a bot).
    pub fn plan_chat(&self, operator: &str, chat: &ChatEnvelope) -> Option<Notification> {
        if chat.message.from_operator(operator) {
            return None;
        }
        if !chat.message.from_bot() {
            return None;
        }
        let bot = chat.message.from.strip_prefix("bot:").unwrap_or(&chat.message.from);
        const MAX_BODY: usize = 240;
        let body = truncate(&chat.message.text, MAX_BODY);
        Some(Notification {
            title: format!("{bot} in {}", chat.conversation),
            body,
            // conversation ids are "team:<name>" / "bot:<name>"; the app's
            // chat route is "#/chat/<kind>/<name>".
            link: Some(format!("#/chat/{}", chat.conversation.replacen(':', "/", 1))),
        })
    }
}

fn truncate(s: &str, max: usize) -> String {
    if s.chars().count() <= max {
        return s.to_string();
    }
    let truncated: String = s.chars().take(max.saturating_sub(1)).collect();
    format!("{truncated}\u{2026}")
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn item(key: &str, title: &str) -> Item {
        serde_json::from_value(json!({
            "key": key, "kind": "x", "title": title,
            "opened": "2026-01-01T00:00:00Z", "severity": "info"
        }))
        .unwrap()
    }

    #[test]
    fn single_open_notifies_individually() {
        let mut p = NotificationPlanner::new();
        let mut opened = Vec::new();
        for it in [item("a", "A"), item("b", "B")] {
            if p.apply_attention(AttentionChange::Opened, it.clone()) {
                opened.push(it);
            }
        }
        let plan = p.plan_attention(&opened);
        assert_eq!(plan.len(), 2);
        assert_eq!(p.open_count(), 2);
    }

    #[test]
    fn burst_of_more_than_three_coalesces() {
        let p = NotificationPlanner::new();
        let items: Vec<_> = (0..5).map(|i| item(&format!("k{i}"), "T")).collect();
        let plan = p.plan_attention(&items);
        assert_eq!(plan.len(), 1);
        assert!(plan[0].title.contains('5'));
        assert!(plan[0].link.is_none());
    }

    #[test]
    fn exactly_three_does_not_coalesce() {
        let p = NotificationPlanner::new();
        let items: Vec<_> = (0..3).map(|i| item(&format!("k{i}"), "T")).collect();
        let plan = p.plan_attention(&items);
        assert_eq!(plan.len(), 3);
    }

    #[test]
    fn reopen_of_already_open_item_is_not_new() {
        let mut p = NotificationPlanner::new();
        assert!(p.apply_attention(AttentionChange::Opened, item("a", "A")));
        assert!(!p.apply_attention(AttentionChange::Opened, item("a", "A changed")));
        assert_eq!(p.open_count(), 1);
    }

    #[test]
    fn updated_replaces_in_place_without_flagging_new() {
        let mut p = NotificationPlanner::new();
        assert!(p.apply_attention(AttentionChange::Opened, item("a", "A")));
        let was_new = p.apply_attention(AttentionChange::Updated, item("a", "A v2"));
        assert!(!was_new);
        assert_eq!(p.open_count(), 1);
    }

    #[test]
    fn resolved_drops_from_open_set() {
        let mut p = NotificationPlanner::new();
        p.apply_attention(AttentionChange::Opened, item("a", "A"));
        p.apply_attention(AttentionChange::Resolved, item("a", "A"));
        assert_eq!(p.open_count(), 0);
    }

    #[test]
    fn chat_from_bot_notifies() {
        let p = NotificationPlanner::new();
        let chat: ChatEnvelope = serde_json::from_value(json!({
            "conversation": "bot:ada",
            "message": {"seq": 1, "at": "t", "from": "bot:ada", "text": "hello there"}
        }))
        .unwrap();
        let n = p.plan_chat("dan", &chat).unwrap();
        assert_eq!(n.title, "ada in bot:ada");
        assert_eq!(n.link.as_deref(), Some("#/chat/bot/ada"));
    }

    #[test]
    fn chat_from_operator_self_does_not_notify() {
        let p = NotificationPlanner::new();
        let chat: ChatEnvelope = serde_json::from_value(json!({
            "conversation": "team:ops",
            "message": {"seq": 1, "at": "t", "from": "operator:dan", "text": "hi"}
        }))
        .unwrap();
        assert!(p.plan_chat("dan", &chat).is_none());
    }

    #[test]
    fn chat_from_other_operator_does_not_notify() {
        let p = NotificationPlanner::new();
        let chat: ChatEnvelope = serde_json::from_value(json!({
            "conversation": "team:ops",
            "message": {"seq": 1, "at": "t", "from": "operator:alice", "text": "hi"}
        }))
        .unwrap();
        assert!(p.plan_chat("dan", &chat).is_none());
    }

    #[test]
    fn long_chat_text_is_truncated() {
        let p = NotificationPlanner::new();
        let long_text = "x".repeat(400);
        let chat: ChatEnvelope = serde_json::from_value(json!({
            "conversation": "bot:ada",
            "message": {"seq": 1, "at": "t", "from": "bot:ada", "text": long_text}
        }))
        .unwrap();
        let n = p.plan_chat("dan", &chat).unwrap();
        assert!(n.body.chars().count() <= 240);
        assert!(n.body.ends_with('\u{2026}'));
    }

    #[test]
    fn burst_collector_not_ready_before_debounce() {
        let mut b = BurstCollector::new();
        let t0 = Instant::now();
        b.push(item("a", "A"), t0, Duration::from_millis(300));
        assert!(!b.ready(t0 + Duration::from_millis(100)));
        assert!(b.ready(t0 + Duration::from_millis(300)));
    }

    #[test]
    fn burst_collector_extends_deadline_on_each_push() {
        let mut b = BurstCollector::new();
        let t0 = Instant::now();
        let debounce = Duration::from_millis(300);
        b.push(item("a", "A"), t0, debounce);
        b.push(item("b", "B"), t0 + Duration::from_millis(200), debounce);
        // 300ms after the first push, but only 100ms after the second: not ready.
        assert!(!b.ready(t0 + Duration::from_millis(300)));
        assert!(b.ready(t0 + Duration::from_millis(500)));
        let taken = b.take();
        assert_eq!(taken.len(), 2);
        assert!(b.is_empty());
        assert!(b.deadline().is_none());
    }

    #[test]
    fn load_snapshot_seeds_open_set() {
        let mut p = NotificationPlanner::new();
        p.load_snapshot(vec![item("a", "A"), item("b", "B")]);
        assert_eq!(p.open_count(), 2);
        assert!(!p.apply_attention(AttentionChange::Opened, item("a", "A")));
    }
}
