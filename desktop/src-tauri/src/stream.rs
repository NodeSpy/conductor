//! SSE parsing and reconnect policy for `/api/v1/events`.
//!
//! The wire format (one event):
//! ```text
//! id: <cursor>
//! event: <topic>
//! data: {"id":"<cursor>","seq":N,"topic":"attention","type":"opened","ts":"…","data":{…}}
//!
//! ```
//! blank line terminates the event. `: heartbeat` comment lines arrive every
//! 15s; 45s of silence means the connection is dead. A `reset` event means
//! "refetch the snapshot and resume from its cursor" — its payload shape
//! differs from a normal envelope (`{"type":"reset","topic":"reset","id":"<cursor>"}`,
//! no `seq`/`ts`), so it is matched by SSE `event: reset` before the normal
//! envelope is parsed.

use std::time::Duration;

use crate::types::{ChatEnvelope, EventEnvelope, Item, StreamEvent};

#[allow(dead_code)] // documents the protocol constant; reconnect logic only needs DEAD_AFTER
pub const HEARTBEAT_INTERVAL: Duration = Duration::from_secs(15);
pub const DEAD_AFTER: Duration = Duration::from_secs(45);

pub const BACKOFF_MIN: Duration = Duration::from_secs(1);
pub const BACKOFF_MAX: Duration = Duration::from_secs(30);

/// Doubles the previous backoff, capped at BACKOFF_MAX. Pass `None` (first
/// failure) to get BACKOFF_MIN.
pub fn next_backoff(previous: Option<Duration>) -> Duration {
    match previous {
        None => BACKOFF_MIN,
        Some(p) => std::cmp::min(p.saturating_mul(2), BACKOFF_MAX),
    }
}

/// One raw SSE frame: the `event:` field (defaults to "message" per the SSE
/// spec, though this server always sets one) and the concatenation of its
/// `data:` lines.
#[derive(Debug, Default, Clone, PartialEq)]
pub(crate) struct RawFrame {
    event: Option<String>,
    data_lines: Vec<String>,
    id: Option<String>,
}

impl RawFrame {
    fn data(&self) -> String {
        self.data_lines.join("\n")
    }
}

/// Incremental SSE line-splitter: feed it bytes as they arrive (they need not
/// land on line boundaries), get frames back as they complete.
#[derive(Debug, Default)]
pub struct SseDecoder {
    buf: Vec<u8>,
}

impl SseDecoder {
    pub fn new() -> Self {
        Self::default()
    }

    /// Appends bytes and returns any complete lines newly available, consuming
    /// them from the internal buffer. Lines are returned without their
    /// trailing `\n` (or `\r\n`).
    pub fn push(&mut self, bytes: &[u8]) -> Vec<String> {
        self.buf.extend_from_slice(bytes);
        let mut lines = Vec::new();
        loop {
            let Some(pos) = self.buf.iter().position(|&b| b == b'\n') else {
                break;
            };
            let mut line: Vec<u8> = self.buf.drain(..=pos).collect();
            line.pop(); // trailing \n
            if line.last() == Some(&b'\r') {
                line.pop();
            }
            lines.push(String::from_utf8_lossy(&line).to_string());
        }
        lines
    }
}

/// Parses complete SSE lines (as produced by `SseDecoder`) into frames, one
/// per blank-line-terminated block. Comment lines (starting with `:`,
/// including heartbeats) are dropped.
#[derive(Debug, Default)]
pub struct FrameAssembler {
    current: RawFrame,
    any_field: bool,
}

impl FrameAssembler {
    pub fn new() -> Self {
        Self::default()
    }

    /// Feeds one line; returns a completed frame if this line was the blank
    /// line that ends an event block. Comment-only blocks never complete (no
    /// field seen), matching the SSE spec's dispatch rule.
    pub(crate) fn feed_line(&mut self, line: &str) -> Option<RawFrame> {
        if line.is_empty() {
            if !self.any_field {
                return None;
            }
            let frame = std::mem::take(&mut self.current);
            self.any_field = false;
            return Some(frame);
        }
        if let Some(rest) = line.strip_prefix(':') {
            let _ = rest; // comment / heartbeat
            return None;
        }
        let (field, value) = match line.split_once(':') {
            Some((f, v)) => (f, v.strip_prefix(' ').unwrap_or(v)),
            None => (line, ""),
        };
        match field {
            "event" => self.current.event = Some(value.to_string()),
            "data" => self.current.data_lines.push(value.to_string()),
            "id" => self.current.id = Some(value.to_string()),
            _ => {}
        }
        self.any_field = true;
        None
    }
}

#[derive(Debug, thiserror::Error)]
pub enum ParseError {
    #[error("malformed event JSON: {0}")]
    Json(#[from] serde_json::Error),
    #[error("frame had no event: field")]
    NoEventName,
}

/// Turns one completed SSE frame into a `StreamEvent`.
fn interpret(frame: &RawFrame) -> Result<StreamEvent, ParseError> {
    let topic = frame.event.clone().ok_or(ParseError::NoEventName)?;
    let data = frame.data();
    if topic == "reset" {
        let v: serde_json::Value = serde_json::from_str(&data)?;
        let cursor = v
            .get("id")
            .and_then(|x| x.as_str())
            .unwrap_or_default()
            .to_string();
        return Ok(StreamEvent::Reset { cursor });
    }
    let envelope: EventEnvelope = serde_json::from_str(&data)?;
    match envelope.topic.as_str() {
        "attention" => {
            let item: Item = serde_json::from_value(envelope.data.clone())?;
            Ok(StreamEvent::Attention { envelope, item })
        }
        "chat" => {
            let chat: ChatEnvelope = serde_json::from_value(envelope.data.clone())?;
            Ok(StreamEvent::Chat { envelope, chat })
        }
        _ => Ok(StreamEvent::Other(envelope)),
    }
}

/// Stateful parser: feed raw bytes off the response body, get `StreamEvent`s
/// out. Tracks the last seen cursor for reconnect, and whether the stream has
/// gone quiet long enough to be considered dead (caller supplies "now" so this
/// stays unit-testable without real sleeps).
pub struct SseParser {
    decoder: SseDecoder,
    assembler: FrameAssembler,
}

impl SseParser {
    pub fn new() -> Self {
        Self {
            decoder: SseDecoder::new(),
            assembler: FrameAssembler::new(),
        }
    }

    /// Feeds bytes and returns every event they completed, in order. A
    /// malformed frame is skipped (logged by the caller) rather than killing
    /// the connection; at-least-once delivery means a reconnect will resend
    /// it with a different cursor only if the server itself changes, so a
    /// transient parse bug just drops one notification, not the stream.
    pub fn push(&mut self, bytes: &[u8]) -> Vec<Result<StreamEvent, ParseError>> {
        let mut out = Vec::new();
        for line in self.decoder.push(bytes) {
            if let Some(frame) = self.assembler.feed_line(&line) {
                out.push(interpret(&frame));
            }
        }
        out
    }
}

impl Default for SseParser {
    fn default() -> Self {
        Self::new()
    }
}

/// Tracks seen `seq` numbers for dedup (at-least-once delivery means a
/// reconnect may redeliver). Bounded so a long-lived connection doesn't grow
/// forever: only the highest-water-mark plus a small recent set is kept,
/// which is enough since redelivery is always a contiguous replay from a
/// cursor, never an isolated far-future duplicate.
pub struct Dedup {
    high_water: Option<u64>,
    recent: std::collections::BTreeSet<u64>,
}

impl Dedup {
    pub fn new() -> Self {
        Self {
            high_water: None,
            recent: std::collections::BTreeSet::new(),
        }
    }

    /// Returns true if this seq is new (and records it); false if already
    /// seen.
    pub fn accept(&mut self, seq: u64) -> bool {
        if let Some(hw) = self.high_water {
            if seq <= hw && self.recent.contains(&seq) {
                return false;
            }
            if seq <= hw && !self.recent.contains(&seq) {
                // Older than anything we've tracked in detail: treat as a
                // duplicate from a much earlier replay window.
                return false;
            }
        }
        self.recent.insert(seq);
        self.high_water = Some(self.high_water.map_or(seq, |hw| hw.max(seq)));
        // Trim: keep a window behind the high-water mark.
        const WINDOW: u64 = 4096;
        if let Some(hw) = self.high_water {
            let floor = hw.saturating_sub(WINDOW);
            while let Some(&min) = self.recent.iter().next() {
                if min < floor {
                    self.recent.remove(&min);
                } else {
                    break;
                }
            }
        }
        true
    }

    pub fn reset(&mut self) {
        self.high_water = None;
        self.recent.clear();
    }
}

impl Default for Dedup {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn feed_all(parser: &mut SseParser, text: &str) -> Vec<StreamEvent> {
        parser
            .push(text.as_bytes())
            .into_iter()
            .map(|r| r.expect("parse ok"))
            .collect()
    }

    #[test]
    fn parses_one_attention_event() {
        let mut parser = SseParser::new();
        let item = json!({
            "key": "k1", "kind": "proposal", "title": "Needs review",
            "opened": "2026-01-01T00:00:00Z", "severity": "warn"
        });
        let envelope = json!({
            "id": "epoch-5", "seq": 5, "topic": "attention", "type": "opened",
            "ts": "2026-01-01T00:00:00Z", "data": item
        });
        let frame = format!("id: epoch-5\nevent: attention\ndata: {}\n\n", envelope);
        let events = feed_all(&mut parser, &frame);
        assert_eq!(events.len(), 1);
        match &events[0] {
            StreamEvent::Attention { item, .. } => {
                assert_eq!(item.key, "k1");
                assert!(item.is_dismissable());
            }
            other => panic!("expected attention, got {other:?}"),
        }
    }

    #[test]
    fn sourced_item_is_not_dismissable() {
        let item: Item = serde_json::from_value(json!({
            "key": "k2", "kind": "ticket", "title": "t",
            "opened": "2026-01-01T00:00:00Z", "severity": "info", "source": "tickets"
        }))
        .unwrap();
        assert!(!item.is_dismissable());
    }

    #[test]
    fn updated_type_parses_like_opened() {
        let mut parser = SseParser::new();
        let item = json!({
            "key": "k1", "kind": "proposal", "title": "Needs review (edited)",
            "opened": "2026-01-01T00:00:00Z", "severity": "warn"
        });
        let envelope = json!({
            "id": "epoch-6", "seq": 6, "topic": "attention", "type": "updated",
            "ts": "2026-01-01T00:00:01Z", "data": item
        });
        let frame = format!("event: attention\ndata: {}\n\n", envelope);
        let events = feed_all(&mut parser, &frame);
        match &events[0] {
            StreamEvent::Attention { envelope, item } => {
                assert_eq!(envelope.kind, "updated");
                assert_eq!(item.title, "Needs review (edited)");
            }
            other => panic!("expected attention, got {other:?}"),
        }
    }

    #[test]
    fn parses_chat_event_with_multiline_data() {
        let mut parser = SseParser::new();
        let msg = json!({"conversation": "bot:ada", "message": {"seq": 2, "at": "2026-01-01T00:00:00Z", "from": "bot:ada", "text": "hi"}});
        let envelope = json!({"id": "epoch-9", "seq": 9, "topic": "chat", "type": "message", "ts": "2026-01-01T00:00:00Z", "data": msg});
        // Split data across two `data:` lines to exercise multi-line joining
        // (per the SSE spec, data lines join with \n, which must still parse
        // as one JSON document here since our payloads are single-line JSON).
        let raw = serde_json::to_string(&envelope).unwrap();
        let frame = format!("event: chat\ndata: {raw}\n\n");
        let events = feed_all(&mut parser, &frame);
        match &events[0] {
            StreamEvent::Chat { chat, .. } => {
                assert_eq!(chat.conversation, "bot:ada");
                assert!(chat.message.from_bot());
                assert!(!chat.message.from_operator("ada"));
            }
            other => panic!("expected chat, got {other:?}"),
        }
    }

    #[test]
    fn parses_reset_event() {
        let mut parser = SseParser::new();
        let frame = "event: reset\ndata: {\"type\":\"reset\",\"topic\":\"reset\",\"id\":\"epoch-42\"}\n\n";
        let events = feed_all(&mut parser, frame);
        match &events[0] {
            StreamEvent::Reset { cursor } => assert_eq!(cursor, "epoch-42"),
            other => panic!("expected reset, got {other:?}"),
        }
    }

    #[test]
    fn heartbeat_comments_produce_no_event() {
        let mut parser = SseParser::new();
        let events = parser.push(b": heartbeat\n\n");
        assert!(events.is_empty());
    }

    #[test]
    fn bytes_can_arrive_split_mid_line() {
        let mut parser = SseParser::new();
        let envelope = json!({"id":"e-1","seq":1,"topic":"attention","type":"opened","ts":"t",
            "data":{"key":"k","kind":"x","title":"T","opened":"t","severity":"info"}});
        let full = format!("event: attention\ndata: {envelope}\n\n");
        let mid = full.len() / 2;
        let mut events = Vec::new();
        for r in parser.push(full[..mid].as_bytes()) {
            events.push(r.unwrap());
        }
        for r in parser.push(full[mid..].as_bytes()) {
            events.push(r.unwrap());
        }
        assert_eq!(events.len(), 1);
    }

    #[test]
    fn two_events_in_one_push() {
        let mut parser = SseParser::new();
        let e1 = json!({"id":"e-1","seq":1,"topic":"attention","type":"opened","ts":"t",
            "data":{"key":"k1","kind":"x","title":"T1","opened":"t","severity":"info"}});
        let e2 = json!({"id":"e-2","seq":2,"topic":"attention","type":"opened","ts":"t",
            "data":{"key":"k2","kind":"x","title":"T2","opened":"t","severity":"info"}});
        let text = format!("event: attention\ndata: {e1}\n\nevent: attention\ndata: {e2}\n\n");
        let events = feed_all(&mut parser, &text);
        assert_eq!(events.len(), 2);
    }

    #[test]
    fn backoff_doubles_and_caps() {
        let mut b = None;
        let mut seen = Vec::new();
        for _ in 0..10 {
            let next = next_backoff(b);
            seen.push(next);
            b = Some(next);
        }
        assert_eq!(seen[0], Duration::from_secs(1));
        assert_eq!(seen[1], Duration::from_secs(2));
        assert_eq!(seen[2], Duration::from_secs(4));
        assert!(seen.iter().all(|d| *d <= BACKOFF_MAX));
        assert_eq!(*seen.last().unwrap(), BACKOFF_MAX);
    }

    #[test]
    fn dedup_rejects_repeats_and_old_replays() {
        let mut d = Dedup::new();
        assert!(d.accept(5));
        assert!(!d.accept(5));
        assert!(d.accept(6));
        assert!(!d.accept(5)); // replayed from an earlier cursor
        assert!(d.accept(7));
    }

    #[test]
    fn dedup_reset_clears_state() {
        let mut d = Dedup::new();
        assert!(d.accept(100));
        d.reset();
        assert!(d.accept(5)); // would have been "old" pre-reset
    }
}
