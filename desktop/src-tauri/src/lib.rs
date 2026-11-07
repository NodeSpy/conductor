mod client;
mod notify;
mod persist;
mod settings;
mod sign;
mod stream;
mod subscribe;
mod tray;
mod types;

use std::path::PathBuf;
use std::sync::Mutex as StdMutex;

use tauri::menu::MenuEvent;
use tauri::tray::{TrayIcon, TrayIconBuilder, TrayIconEvent};
use tauri::{AppHandle, Emitter, Manager, WebviewUrl, WebviewWindowBuilder, Wry};
use tauri_plugin_autostart::ManagerExt as _;
use tauri_plugin_notification::NotificationExt;
use tauri_plugin_opener::OpenerExt;
use tokio::sync::Mutex as AsyncMutex;

use client::{ApprovalKind, ApprovalView, Client, ClientError};
use settings::Settings;
use types::AttentionSnapshot;

struct AppState {
    config_dir: PathBuf,
    data_dir: PathBuf,
    settings: StdMutex<Settings>,
    /// The one shared signed-in session, built by `complete_setup`/startup
    /// and used both by the background subscription (subscribe.rs) and by
    /// every Tauri command below — never a separate client per call.
    client: AsyncMutex<Option<Client>>,
    subscription: AsyncMutex<Option<subscribe::SubscriptionHandle>>,
    tray: StdMutex<Option<TrayIcon<Wry>>>,
}

impl AppState {
    fn snapshot_settings(&self) -> Settings {
        self.settings.lock().unwrap().clone()
    }

    async fn current_client(&self) -> Option<Client> {
        self.client.lock().await.clone()
    }
}

#[tauri::command]
fn get_settings(state: tauri::State<AppState>) -> Settings {
    state.snapshot_settings()
}

#[tauri::command]
fn default_ssh_key() -> Option<String> {
    settings::default_ssh_key()
}

#[tauri::command]
fn save_settings(state: tauri::State<AppState>, settings: Settings) -> Result<(), String> {
    settings::save(&state.config_dir, &settings).map_err(|e| e.to_string())?;
    *state.settings.lock().unwrap() = settings;
    Ok(())
}

/// Builds the shared client, proves it works with a real sign-in, and
/// (re)starts the background subscription. Called after the setup/settings
/// form saves. Unlike the old remote-dashboard design, this never navigates
/// any window anywhere — the main window only ever shows this app's own UI
/// (`desktop/app`), and the frontend moves on to `#/needs` itself once this
/// resolves.
#[tauri::command]
async fn complete_setup(app: AppHandle, state: tauri::State<'_, AppState>) -> Result<(), String> {
    let settings = state.snapshot_settings();
    if !settings.is_configured() {
        return Err("settings are incomplete".to_string());
    }

    // Prove the settings work before saying so: a real sign-in reaches the
    // daemon, names the operator and signs with the key, and each failure
    // gets a message that says which.
    let probe = Client::new(&settings.daemon_url, &settings.ssh_public_key_path).map_err(|e| e.to_string())?;
    match tokio::time::timeout(std::time::Duration::from_secs(30), probe.sign_in(&settings.operator)).await {
        Err(_) => return Err(format!("{} did not answer within 30s.", settings.daemon_url)),
        Ok(Err(e)) => return Err(setup_error(&settings.daemon_url, &e)),
        Ok(Ok(())) => {}
    }

    let _ = app.autolaunch().enable();
    restart_subscription(&app, &state, settings.clone(), Some(probe)).await;
    Ok(())
}

/// A setup failure in words that say what to fix.
fn setup_error(url: &str, e: &client::ClientError) -> String {
    use client::ClientError::*;
    match e {
        Network(err) if err.is_connect() || err.is_timeout() => format!(
            "Can't reach {url}. Check the daemon is running with its dashboard enabled, \
             listening on this address (dashboard.listen: [localhost, tailscale] for a remote one)."
        ),
        Network(err) => format!("Can't talk to {url}: {err}"),
        Sign(err) => format!("Couldn't sign with your SSH key: {err}"),
        Daemon(d) => format!("The daemon refused sign-in: {}", d.message),
        Status(code, body) => format!("The daemon refused sign-in ({code}): {body}"),
        Unauthorized | SignIn(_) => format!(
            "The daemon refused sign-in. Check the operator name, and that operators.<name>.ssh_key \
             in its config is this key."
        ),
    }
}

/// (Re)builds the shared client (unless one is passed in, already proven by
/// `complete_setup`'s probe), stores it in `AppState`, and (re)starts the
/// background subscription with a clone of it.
async fn restart_subscription(app: &AppHandle, state: &tauri::State<'_, AppState>, settings: Settings, built: Option<Client>) {
    let mut sub_guard = state.subscription.lock().await;
    if let Some(old) = sub_guard.take() {
        old.stop().await;
    }
    let Some(tray) = state.tray.lock().unwrap().clone() else {
        tracing::error!("no tray icon yet, cannot start subscription");
        return;
    };
    let client = match built {
        Some(c) => c,
        None => match Client::new(&settings.daemon_url, &settings.ssh_public_key_path) {
            Ok(c) => c,
            Err(e) => {
                tracing::error!(error = %e, "could not build http client");
                return;
            }
        },
    };
    *state.client.lock().await = Some(client.clone());
    let handle = subscribe::start(
        app.clone(),
        client,
        settings.operator.clone(),
        state.data_dir.clone(),
        tray,
    );
    *sub_guard = Some(handle);
}

/// Runs `call` against the shared client, signing in again once and retrying
/// if the session had expired (`ClientError::Unauthorized`). Every command
/// below that talks to the daemon goes through this, so there is one retry
/// policy in one place.
async fn with_client<T, Fut>(state: &tauri::State<'_, AppState>, call: impl Fn(Client) -> Fut) -> Result<T, String>
where
    Fut: std::future::Future<Output = Result<T, ClientError>>,
{
    let client = state
        .current_client()
        .await
        .ok_or_else(|| "not connected to a daemon yet".to_string())?;
    match call(client.clone()).await {
        Err(ClientError::Unauthorized) => {
            let operator = state.snapshot_settings().operator;
            client.sign_in(&operator).await.map_err(|e| e.to_command_error())?;
            call(client).await.map_err(|e| e.to_command_error())
        }
        Ok(v) => Ok(v),
        Err(e) => Err(e.to_command_error()),
    }
}

#[tauri::command]
async fn attention(state: tauri::State<'_, AppState>) -> Result<AttentionSnapshot, String> {
    with_client(&state, |c| async move { c.attention_snapshot().await }).await
}

#[tauri::command]
async fn dismiss(state: tauri::State<'_, AppState>, key: String) -> Result<(), String> {
    with_client(&state, move |c| {
        let key = key.clone();
        async move { c.dismiss(&key).await }
    })
    .await
}

#[tauri::command]
async fn approval_view(state: tauri::State<'_, AppState>, kind: String, bot: Option<String>, id: String) -> Result<ApprovalView, String> {
    let kind = ApprovalKind::parse(&kind)?;
    with_client(&state, move |c| {
        let (bot, id) = (bot.clone(), id.clone());
        async move { c.approval_view(kind, bot.as_deref(), &id).await }
    })
    .await
}

#[tauri::command]
async fn decide(
    state: tauri::State<'_, AppState>,
    kind: String,
    bot: Option<String>,
    id: String,
    decision: String,
    reason: Option<String>,
) -> Result<(), String> {
    let kind = ApprovalKind::parse(&kind)?;
    with_client(&state, move |c| {
        let (bot, id, decision, reason) = (bot.clone(), id.clone(), decision.clone(), reason.clone());
        async move { c.decide(kind, bot.as_deref(), &id, &decision, reason.as_deref()).await }
    })
    .await
}

#[tauri::command]
async fn conversations(state: tauri::State<'_, AppState>) -> Result<serde_json::Value, String> {
    with_client(&state, |c| async move { c.conversations().await }).await
}

#[tauri::command]
async fn messages(state: tauri::State<'_, AppState>, id: String, after: Option<u64>, mark: Option<bool>) -> Result<serde_json::Value, String> {
    let after = after.unwrap_or(0);
    let mark = mark.unwrap_or(true);
    with_client(&state, move |c| {
        let id = id.clone();
        async move { c.messages(&id, after, mark).await }
    })
    .await
}

#[tauri::command]
async fn send_message(
    state: tauri::State<'_, AppState>,
    id: String,
    text: String,
    to: Option<Vec<String>>,
    everyone: Option<bool>,
) -> Result<serde_json::Value, String> {
    let everyone = everyone.unwrap_or(false);
    with_client(&state, move |c| {
        let (id, text, to) = (id.clone(), text.clone(), to.clone());
        async move { c.send_message(&id, &text, to.as_deref(), everyone).await }
    })
    .await
}

#[tauri::command]
async fn mark_read(state: tauri::State<'_, AppState>, id: String, upto: u64) -> Result<(), String> {
    with_client(&state, move |c| {
        let id = id.clone();
        async move { c.mark_read(&id, upto).await }
    })
    .await
}

#[tauri::command]
async fn bots(state: tauri::State<'_, AppState>) -> Result<serde_json::Value, String> {
    with_client(&state, |c| async move { c.bots().await }).await
}

#[tauri::command]
async fn bot(state: tauri::State<'_, AppState>, name: String) -> Result<serde_json::Value, String> {
    with_client(&state, move |c| {
        let name = name.clone();
        async move { c.bot(&name).await }
    })
    .await
}

#[tauri::command]
async fn bot_proposals(state: tauri::State<'_, AppState>, name: String) -> Result<serde_json::Value, String> {
    with_client(&state, move |c| {
        let name = name.clone();
        async move { c.bot_proposals(&name).await }
    })
    .await
}

/// Opens a URL in the system browser (the tray's "Open dashboard in
/// browser", and a Needs you row for something this app doesn't render
/// in-app, e.g. a run's page).
#[tauri::command]
fn open_in_browser(app: AppHandle, url: String) -> Result<(), String> {
    app.opener().open_url(url, None::<&str>).map_err(|e| e.to_string())
}

fn open_settings_window(app: &AppHandle) {
    if let Some(w) = app.get_webview_window("settings") {
        let _ = w.show();
        let _ = w.set_focus();
        return;
    }
    let _ = WebviewWindowBuilder::new(app, "settings", WebviewUrl::App("index.html#/setup".into()))
        .title("Conductor Settings")
        .inner_size(560.0, 640.0)
        .build();
}

fn show_main_window(app: &AppHandle) {
    if let Some(w) = app.get_webview_window("main") {
        let _ = w.show();
        let _ = w.set_focus();
    }
}

/// Opens the configured daemon's dashboard in the system browser (the tray's
/// "Open dashboard in browser" — the app's own window never loads it).
fn open_dashboard_in_browser(app: &AppHandle) {
    let settings = app.state::<AppState>().snapshot_settings();
    if settings.daemon_url.is_empty() {
        return;
    }
    let _ = app.opener().open_url(settings.daemon_url, None::<&str>);
}

/// Tells the main window to navigate to an in-app hash route (e.g.
/// `#/needs/<key>`, `#/chat/<kind>/<name>`) and shows it — used both for a
/// clicked notification and, in future, any other in-app deep link. The
/// window itself never loads a remote origin; this only ever sets its own
/// location's hash via a window event the frontend listens for
/// (`conductor://navigate`, see src/state.ts).
fn open_link(app: &AppHandle, link: Option<&str>) {
    if let Some(window) = app.get_webview_window("main") {
        if let Some(link) = link {
            let _ = app.emit_to("main", "conductor://navigate", link);
        }
        let _ = window.show();
        let _ = window.set_focus();
    }
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .init();

    tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            show_main_window(app);
        }))
        .plugin(tauri_plugin_autostart::init(
            tauri_plugin_autostart::MacosLauncher::LaunchAgent,
            None,
        ))
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_opener::init())
        .invoke_handler(tauri::generate_handler![
            get_settings,
            save_settings,
            default_ssh_key,
            complete_setup,
            attention,
            dismiss,
            approval_view,
            decide,
            conversations,
            messages,
            send_message,
            mark_read,
            bots,
            bot,
            bot_proposals,
            open_in_browser,
        ])
        .setup(|app| {
            let handle = app.handle().clone();
            let config_dir = app.path().app_config_dir()?;
            let data_dir = app.path().app_data_dir()?;
            let settings = settings::load(&config_dir);

            let tray_menu = tray::build_menu(&handle, 0)?;
            let tray = TrayIconBuilder::new()
                .menu(&tray_menu)
                .icon(app.default_window_icon().cloned().unwrap_or_else(|| {
                    tauri::image::Image::new(&[0, 0, 0, 0], 1, 1)
                }))
                // The icon is the full-colour app icon, not a monochrome
                // template, so macOS must draw it as is.
                .icon_as_template(false)
                .tooltip("Conductor")
                .on_menu_event(move |app, event: MenuEvent| {
                    let app = app.clone();
                    tray::handle_menu_event(
                        &app,
                        &event,
                        || show_main_window(&app),
                        || open_dashboard_in_browser(&app),
                        || open_settings_window(&app),
                    );
                })
                .on_tray_icon_event(|tray, event| {
                    if let TrayIconEvent::Click { .. } = event {
                        show_main_window(tray.app_handle());
                    }
                })
                .build(app)?;

            app.manage(AppState {
                config_dir: config_dir.clone(),
                data_dir: data_dir.clone(),
                settings: StdMutex::new(settings.clone()),
                client: AsyncMutex::new(None),
                subscription: AsyncMutex::new(None),
                tray: StdMutex::new(Some(tray)),
            });

            // Clicking a notification body ("tap") shows the main window and,
            // best-effort, navigates it to the in-app route the event
            // carried: we stash it in the notification's `extra` payload
            // when firing it (subscribe.rs/notify.rs), but not every desktop
            // notification backend round-trips `extra` back to the click
            // handler (see README).
            let _ = app.notification().on_action(move |perf| {
                if perf.action_id() == "tap" {
                    let link = perf
                        .notification()
                        .and_then(|n| n.extra().get("link"))
                        .and_then(|v| v.as_str())
                        .map(|s| s.to_string());
                    open_link(&handle, link.as_deref());
                }
            });

            if settings.is_configured() {
                let app2 = app.handle().clone();
                tauri::async_runtime::spawn(async move {
                    let state = app2.state::<AppState>();
                    let settings = state.snapshot_settings();
                    restart_subscription(&app2, &state, settings, None).await;
                });
            }

            // Closing the main window hides it instead: the app keeps running
            // in the tray and the subscription keeps watching.
            if let Some(window) = app.get_webview_window("main") {
                let w = window.clone();
                window.on_window_event(move |event| {
                    if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                        api.prevent_close();
                        let _ = w.hide();
                    }
                });
            }

            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("error while building conductor desktop")
        .run(|app, event| {
            // macOS: clicking the Dock icon while the window is hidden
            // brings it back.
            #[cfg(target_os = "macos")]
            if let tauri::RunEvent::Reopen { .. } = event {
                show_main_window(app);
            }
            #[cfg(not(target_os = "macos"))]
            let _ = (app, event);
        });
}
