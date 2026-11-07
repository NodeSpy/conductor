//! The background subscription: signs in, takes the attention snapshot, then
//! follows `/api/v1/events` for `attention` and `chat`, reconnecting with
//! backoff and always resuming from the last cursor. Runs for as long as the
//! app does, independent of whether the main window is visible.

use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use futures_util::StreamExt;
use tauri::tray::TrayIcon;
use tauri::{AppHandle, Emitter, Manager, Runtime};
use tauri_plugin_notification::NotificationExt;
use tokio_util::sync::CancellationToken;

use crate::client::{Client, ClientError};
use crate::notify::{AttentionChange, BurstCollector, Notification, NotificationPlanner};
use crate::persist;
use crate::stream::{next_backoff, Dedup, SseParser};
use crate::tray;
use crate::types::StreamEvent;

/// The app's own screens (Needs you, Chat, Bots) need live updates too, not
/// just notifications, so the subscription now also follows `ticket` (a
/// bot's cases, which the Bots screens surface as pending-proposal/open-case
/// counts) alongside `attention` and `chat`.
const TOPICS: &str = "attention,chat,ticket";

/// The window event every parsed envelope is emitted to (see `emit_event`),
/// picked up by `src/state.ts`'s `wireLiveUpdates`. A `reset` is emitted the
/// same way with `type: "reset"` so the frontend can refetch snapshots
/// without this module knowing anything about the UI's state.
const EVENT_NAME: &str = "conductor://event";
const BURST_DEBOUNCE: Duration = Duration::from_millis(400);
const SIGNIN_MAX_ATTEMPTS: usize = 6;

pub struct SubscriptionHandle {
    cancel: CancellationToken,
    join: tauri::async_runtime::JoinHandle<()>,
}

impl SubscriptionHandle {
    pub async fn stop(self) {
        self.cancel.cancel();
        let _ = self.join.await;
    }
}

/// Starts the subscription for one (client, operator) pair. `data_dir` is
/// where the cursor is persisted. Notifications navigate the main window
/// (shown if hidden) to the daemon origin + the event's hash-route link.
pub fn start<R: Runtime>(
    app: AppHandle<R>,
    client: Client,
    operator: String,
    data_dir: PathBuf,
    tray: TrayIcon<R>,
) -> SubscriptionHandle {
    let cancel = CancellationToken::new();
    let token = cancel.clone();
    let join = tauri::async_runtime::spawn(async move {
        run(app, client, operator, data_dir, tray, token).await;
    });
    SubscriptionHandle { cancel, join }
}

async fn run<R: Runtime>(
    app: AppHandle<R>,
    client: Client,
    operator: String,
    data_dir: PathBuf,
    tray: TrayIcon<R>,
    cancel: CancellationToken,
) {
    if cancel.is_cancelled() {
        return;
    }
    if !sign_in_with_backoff(&app, &client, &operator, &cancel).await {
        return; // cancelled, or gave up and already notified
    }

    let planner = Arc::new(std::sync::Mutex::new(NotificationPlanner::new()));
    let dedup = Arc::new(std::sync::Mutex::new(Dedup::new()));
    let unauthorized = Arc::new(AtomicBool::new(false));

    let mut cursor = persist::load_cursor(&data_dir);
    let mut backoff: Option<Duration> = None;

    loop {
        if cancel.is_cancelled() {
            return;
        }

        // (Re)fetch the snapshot whenever we have no cursor yet, or just hit a
        // reset / an error that calls for one.
        if cursor.is_none() || unauthorized.load(Ordering::SeqCst) {
            if unauthorized.swap(false, Ordering::SeqCst)
                && !sign_in_with_backoff(&app, &client, &operator, &cancel).await
            {
                return;
            }
            match client.attention_snapshot().await {
                Ok(snap) => {
                    planner.lock().unwrap().load_snapshot(snap.items);
                    cursor = Some(snap.cursor);
                    tray::update_count(
                        &app,
                        &tray,
                        planner.lock().unwrap().open_count(),
                        app.get_webview_window("main").as_ref(),
                    );
                    persist::save_cursor(&data_dir, cursor.as_deref().unwrap_or_default()).ok();
                }
                Err(ClientError::Unauthorized) => {
                    unauthorized.store(true, Ordering::SeqCst);
                    continue;
                }
                Err(e) => {
                    tracing::warn!(error = %e, "attention snapshot failed, retrying");
                    if cancel_sleep(&cancel, next_backoff(backoff)).await {
                        return;
                    }
                    backoff = Some(next_backoff(backoff));
                    continue;
                }
            }
        }

        match stream_once(
            &app,
            &client,
            &operator,
            cursor.clone(),
            &planner,
            &dedup,
            &tray,
            &data_dir,
            &cancel,
        )
        .await
        {
            StreamOutcome::Cancelled => return,
            StreamOutcome::Reset => {
                cursor = None; // forces a fresh snapshot next loop
                backoff = None;
                // A reset can mean the journal was recreated (new epoch): old
                // seqs carry no guarantees against the new ones.
                dedup.lock().unwrap().reset();
            }
            StreamOutcome::Unauthorized => {
                unauthorized.store(true, Ordering::SeqCst);
                backoff = None;
            }
            StreamOutcome::Disconnected { last_cursor } => {
                if let Some(c) = last_cursor {
                    cursor = Some(c);
                }
                if cancel_sleep(&cancel, next_backoff(backoff)).await {
                    return;
                }
                backoff = Some(next_backoff(backoff));
            }
        }
    }
}

enum StreamOutcome {
    Cancelled,
    Reset,
    Unauthorized,
    Disconnected { last_cursor: Option<String> },
}

/// Sleeps for `d` unless cancelled first; returns true if cancelled.
async fn cancel_sleep(cancel: &CancellationToken, d: Duration) -> bool {
    tokio::select! {
        _ = cancel.cancelled() => true,
        _ = tokio::time::sleep(d) => false,
    }
}

#[allow(clippy::too_many_arguments)]
async fn stream_once<R: Runtime>(
    app: &AppHandle<R>,
    client: &Client,
    operator: &str,
    cursor: Option<String>,
    planner: &Arc<std::sync::Mutex<NotificationPlanner>>,
    dedup: &Arc<std::sync::Mutex<Dedup>>,
    tray: &TrayIcon<R>,
    data_dir: &PathBuf,
    cancel: &CancellationToken,
) -> StreamOutcome {
    let resp = match client.event_stream_request(TOPICS, cursor.as_deref()).await {
        Ok(r) => r,
        Err(ClientError::Unauthorized) => return StreamOutcome::Unauthorized,
        Err(e) => {
            tracing::warn!(error = %e, "event stream connect failed");
            return StreamOutcome::Disconnected { last_cursor: cursor };
        }
    };

    let mut body = resp.bytes_stream();
    let mut parser = SseParser::new();
    let mut burst = BurstCollector::new();
    let mut last_cursor = cursor;
    let heartbeat_deadline = || Instant::now() + crate::stream::DEAD_AFTER;
    let mut dead_at = heartbeat_deadline();

    loop {
        let sleep_until = burst.deadline().unwrap_or_else(|| dead_at);
        tokio::select! {
            _ = cancel.cancelled() => return StreamOutcome::Cancelled,
            _ = tokio::time::sleep_until(sleep_until.into()) => {
                if burst.ready(Instant::now()) {
                    flush_burst(app, planner, &mut burst);
                }
                if Instant::now() >= dead_at {
                    tracing::warn!("event stream silent for too long, reconnecting");
                    return StreamOutcome::Disconnected { last_cursor };
                }
            }
            chunk = body.next() => {
                let Some(chunk) = chunk else {
                    return StreamOutcome::Disconnected { last_cursor };
                };
                let bytes = match chunk {
                    Ok(b) => b,
                    Err(e) => {
                        tracing::warn!(error = %e, "event stream read error");
                        return StreamOutcome::Disconnected { last_cursor };
                    }
                };
                dead_at = heartbeat_deadline();
                for parsed in parser.push(&bytes) {
                    match parsed {
                        Ok(StreamEvent::Reset { .. }) => {
                            flush_burst(app, planner, &mut burst);
                            emit_event(app, "reset", "reset");
                            return StreamOutcome::Reset;
                        }
                        Ok(StreamEvent::Attention { envelope, item }) => {
                            last_cursor = Some(envelope.id.clone());
                            persist::save_cursor(data_dir, &envelope.id).ok();
                            let mut d = dedup.lock().unwrap();
                            if !d.accept(envelope.seq) { continue; }
                            drop(d);
                            let change = match envelope.kind.as_str() {
                                "opened" => AttentionChange::Opened,
                                "resolved" => AttentionChange::Resolved,
                                "updated" => AttentionChange::Updated,
                                other => {
                                    tracing::debug!(kind = %other, "unknown attention event type, treating as update");
                                    AttentionChange::Updated
                                }
                            };
                            let is_new = planner.lock().unwrap().apply_attention(change, item.clone());
                            tray::update_count(app, tray, planner.lock().unwrap().open_count(), app.get_webview_window("main").as_ref());
                            if change == AttentionChange::Opened && is_new {
                                burst.push(item, Instant::now(), BURST_DEBOUNCE);
                            }
                            emit_event(app, &envelope.topic, &envelope.kind);
                        }
                        Ok(StreamEvent::Chat { envelope, chat }) => {
                            last_cursor = Some(envelope.id.clone());
                            persist::save_cursor(data_dir, &envelope.id).ok();
                            let mut d = dedup.lock().unwrap();
                            if !d.accept(envelope.seq) { continue; }
                            drop(d);
                            if let Some(n) = planner.lock().unwrap().plan_chat(operator, &chat) {
                                fire(app, &n);
                            }
                            emit_event(app, &envelope.topic, &envelope.kind);
                        }
                        Ok(StreamEvent::Other(envelope)) => {
                            last_cursor = Some(envelope.id.clone());
                            // Covers topics this module has no notification logic
                            // for (e.g. `ticket`): the UI still wants to know.
                            emit_event(app, &envelope.topic, &envelope.kind);
                        }
                        Err(e) => {
                            tracing::warn!(error = %e, "could not parse an event, skipping it");
                        }
                    }
                }
            }
        }
    }
}

fn flush_burst<R: Runtime>(
    app: &AppHandle<R>,
    planner: &Arc<std::sync::Mutex<NotificationPlanner>>,
    burst: &mut BurstCollector,
) {
    if burst.is_empty() {
        return;
    }
    let items = burst.take();
    let plan = planner.lock().unwrap().plan_attention(&items);
    for n in plan {
        fire(app, &n);
    }
}

/// Emits one event to the main window so the app's own screens (not just
/// notifications) can refetch live, off the same resumable journal stream
/// the tray uses. `src/state.ts`'s `wireLiveUpdates` listens for this.
#[derive(serde::Serialize, Clone)]
struct UiEvent<'a> {
    topic: &'a str,
    #[serde(rename = "type")]
    kind: &'a str,
}

fn emit_event<R: Runtime>(app: &AppHandle<R>, topic: &str, kind: &str) {
    if let Err(e) = app.emit(EVENT_NAME, UiEvent { topic, kind }) {
        tracing::warn!(error = %e, "could not emit event to the window");
    }
}

fn fire<R: Runtime>(app: &AppHandle<R>, n: &Notification) {
    let mut builder = app.notification().builder().title(&n.title);
    if !n.body.is_empty() {
        builder = builder.body(&n.body);
    }
    if let Some(link) = &n.link {
        // Best-effort: see lib.rs's on_action handler and the README note on
        // whether a given desktop notification backend round-trips `extra`.
        builder = builder.extra("link", link.clone());
    }
    if let Err(e) = builder.show() {
        tracing::warn!(error = %e, "could not show notification");
    }
}

/// Signs in, retrying with backoff; gives up (and notifies "sign-in failed")
/// after `SIGNIN_MAX_ATTEMPTS`. Returns false if cancelled or given up.
async fn sign_in_with_backoff<R: Runtime>(
    app: &AppHandle<R>,
    client: &Client,
    operator: &str,
    cancel: &CancellationToken,
) -> bool {
    let mut backoff: Option<Duration> = None;
    for attempt in 0..SIGNIN_MAX_ATTEMPTS {
        if cancel.is_cancelled() {
            return false;
        }
        match client.sign_in(operator).await {
            Ok(()) => return true,
            Err(e) => {
                tracing::warn!(error = %e, attempt, "sign-in failed");
                if attempt + 1 >= SIGNIN_MAX_ATTEMPTS {
                    break;
                }
                if cancel_sleep(cancel, next_backoff(backoff)).await {
                    return false;
                }
                backoff = Some(next_backoff(backoff));
            }
        }
    }
    fire(
        app,
        &Notification {
            title: "Sign-in failed".to_string(),
            body: "Conductor could not sign in with your SSH key. Check Settings.".to_string(),
            link: None,
        },
    );
    false
}
