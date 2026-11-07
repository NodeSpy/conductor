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

use tauri::ipc::CapabilityBuilder;
use tauri::menu::MenuEvent;
use tauri::tray::{TrayIcon, TrayIconBuilder, TrayIconEvent};
use tauri::{AppHandle, Manager, WebviewUrl, WebviewWindowBuilder, Wry};
use tauri_plugin_autostart::ManagerExt as _;
use tauri_plugin_dialog::{DialogExt, MessageDialogButtons, MessageDialogKind};
use tauri_plugin_notification::NotificationExt;
use tokio::sync::Mutex as AsyncMutex;

use client::Client;
use settings::Settings;

const REMOTE_CAPABILITY_ID: &str = "remote-daemon";

struct AppState {
    config_dir: PathBuf,
    data_dir: PathBuf,
    settings: StdMutex<Settings>,
    subscription: AsyncMutex<Option<subscribe::SubscriptionHandle>>,
    tray: StdMutex<Option<TrayIcon<Wry>>>,
}

impl AppState {
    fn snapshot_settings(&self) -> Settings {
        self.settings.lock().unwrap().clone()
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

/// Grants the remote daemon origin the one `ssh_sign` command, navigates the
/// main window there, and (re)starts the background subscription. Called
/// after the setup/settings form saves.
#[tauri::command]
async fn complete_setup(app: AppHandle, state: tauri::State<'_, AppState>) -> Result<(), String> {
    let settings = state.snapshot_settings();
    if !settings.is_configured() {
        return Err("settings are incomplete".to_string());
    }
    let origin = settings
        .daemon_origin()
        .ok_or_else(|| "daemon URL is not a valid URL".to_string())?;

    // Dynamic ACL: grant the remote page at this origin (and only this
    // origin) the ssh_sign command on the main window. Re-running this adds a
    // fresh capability each time the daemon URL changes; stale ones from a
    // previous origin are harmless (Tauri resolves by matching URL) but we
    // use a stable identifier so a repeat call just redefines it.
    let capability = CapabilityBuilder::new(REMOTE_CAPABILITY_ID)
        .remote(origin.clone())
        .window("main")
        .permission("allow-ssh-sign");
    app.add_capability(capability).map_err(|e| e.to_string())?;

    let _ = app.autolaunch().enable();

    restart_subscription(&app, &state, settings.clone()).await;

    if let Some(window) = app.get_webview_window("main") {
        let url = url::Url::parse(&settings.daemon_url).map_err(|e| e.to_string())?;
        window.navigate(url).map_err(|e| e.to_string())?;
        let _ = window.show();
        let _ = window.set_focus();
    }
    Ok(())
}

async fn restart_subscription(app: &AppHandle, state: &tauri::State<'_, AppState>, settings: Settings) {
    let mut guard = state.subscription.lock().await;
    if let Some(old) = guard.take() {
        old.stop().await;
    }
    let Some(tray) = state.tray.lock().unwrap().clone() else {
        tracing::error!("no tray icon yet, cannot start subscription");
        return;
    };
    let client = match Client::new(&settings.daemon_url, &settings.ssh_public_key_path) {
        Ok(c) => c,
        Err(e) => {
            tracing::error!(error = %e, "could not build http client");
            return;
        }
    };
    let handle = subscribe::start(
        app.clone(),
        client,
        settings.operator.clone(),
        state.data_dir.clone(),
        tray,
    );
    *guard = Some(handle);
}

/// The one command the daemon's remote dashboard page may call (see
/// `capabilities/default.json` for the static/local grant and
/// `complete_setup` for the runtime remote grant that scopes it to the
/// configured daemon origin only). Confirms with a native dialog naming the
/// namespace and the exact text before signing — this is a page-initiated
/// sign-in or step-up, unlike the app's own background sign-in in
/// `client::Client::sign_in`, which signs without a prompt.
#[tauri::command]
async fn ssh_sign(
    app: AppHandle,
    webview: tauri::Webview,
    state: tauri::State<'_, AppState>,
    namespace: String,
    message: String,
) -> Result<String, String> {
    if !sign::namespace_allowed(&namespace) {
        return Err(format!("refusing to sign in namespace {namespace:?}"));
    }
    let settings = state.snapshot_settings();

    // Defense in depth, on top of the capability system: the runtime
    // "remote-daemon" capability (complete_setup) scopes ssh_sign to the
    // configured origin, but tauri::ipc::RuntimeAuthority::add_capability
    // only ever appends — reconfiguring to a new daemon does not revoke the
    // old origin's grant for the life of the running app. So check the
    // *current* settings against the calling webview's *current* URL on
    // every call, independent of whatever capability grants have
    // accumulated.
    let caller_url = webview.url().map_err(|e| e.to_string())?;
    if !settings.allows_origin(&caller_url) {
        tracing::warn!(url = %caller_url, "ssh_sign called from an origin that isn't the configured daemon, refusing");
        return Err("ssh_sign is not available to this origin".to_string());
    }

    let pubkey_path = settings.ssh_public_key_path;
    if pubkey_path.trim().is_empty() {
        return Err("no SSH key configured".to_string());
    }

    let (tx, rx) = tokio::sync::oneshot::channel();
    let kind = if namespace == sign::STEPUP_NAMESPACE {
        "step-up"
    } else {
        "sign-in"
    };
    app.dialog()
        .message(format!("Namespace: {namespace}\n\n{message}"))
        .title(format!("Sign this {kind} request with your SSH key?"))
        .kind(MessageDialogKind::Info)
        .buttons(MessageDialogButtons::OkCancelCustom(
            "Sign".to_string(),
            "Cancel".to_string(),
        ))
        .show(move |confirmed| {
            let _ = tx.send(confirmed);
        });
    let confirmed = rx.await.unwrap_or(false);
    if !confirmed {
        return Err("signing was cancelled".to_string());
    }

    sign::ssh_sign(&pubkey_path, &namespace, &message)
        .await
        .map_err(|e| e.to_string())
}

fn open_settings_window(app: &AppHandle) {
    if let Some(w) = app.get_webview_window("settings") {
        let _ = w.show();
        let _ = w.set_focus();
        return;
    }
    let _ = WebviewWindowBuilder::new(app, "settings", WebviewUrl::App("setup.html".into()))
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

/// Navigates the main window to a dashboard hash route and shows it — used
/// both for a clicked notification and the tray's "Open".
fn open_link(app: &AppHandle, link: Option<&str>) {
    if let Some(window) = app.get_webview_window("main") {
        if let Some(link) = link {
            let settings = app.state::<AppState>().snapshot_settings();
            if let Some(origin) = settings.daemon_origin() {
                let full = format!("{origin}/{}", link.trim_start_matches('/'));
                if let Ok(url) = url::Url::parse(&full) {
                    let _ = window.navigate(url);
                }
            }
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
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_opener::init())
        .invoke_handler(tauri::generate_handler![
            get_settings,
            save_settings,
            default_ssh_key,
            complete_setup,
            ssh_sign,
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
                subscription: AsyncMutex::new(None),
                tray: StdMutex::new(Some(tray)),
            });

            // Clicking a notification body ("tap") shows the main window and,
            // best-effort, navigates to the link it carried: we stash the
            // link in the notification's `extra` payload when firing it
            // (subscribe.rs), but not every desktop notification backend
            // round-trips `extra` back to the click handler (see README).
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
                    if let Some(origin) = state.snapshot_settings().daemon_origin() {
                        let capability = CapabilityBuilder::new(REMOTE_CAPABILITY_ID)
                            .remote(origin)
                            .window("main")
                            .permission("allow-ssh-sign");
                        let _ = app2.add_capability(capability);
                    }
                    let settings = state.snapshot_settings();
                    restart_subscription(&app2, &state, settings.clone()).await;
                    if let Some(window) = app2.get_webview_window("main") {
                        if let Ok(url) = url::Url::parse(&settings.daemon_url) {
                            let _ = window.navigate(url);
                        }
                    }
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
