//! Tray icon and menu: Open / "N need you" / Settings / Quit. The count is
//! updated from the subscription loop as the open-attention set changes.

use tauri::menu::{Menu, MenuEvent, MenuItem};
use tauri::tray::TrayIcon;
use tauri::{AppHandle, Runtime};

pub const MENU_OPEN: &str = "open";
pub const MENU_COUNT: &str = "count";
pub const MENU_SETTINGS: &str = "settings";
pub const MENU_QUIT: &str = "quit";

pub fn build_menu<R: Runtime>(app: &AppHandle<R>, open_count: usize) -> tauri::Result<Menu<R>> {
    let open = MenuItem::with_id(app, MENU_OPEN, "Open", true, None::<&str>)?;
    let count = MenuItem::with_id(app, MENU_COUNT, count_label(open_count), false, None::<&str>)?;
    let settings = MenuItem::with_id(app, MENU_SETTINGS, "Settings", true, None::<&str>)?;
    let quit = MenuItem::with_id(app, MENU_QUIT, "Quit", true, None::<&str>)?;
    Menu::with_items(app, &[&open, &count, &settings, &quit])
}

pub fn count_label(n: usize) -> String {
    match n {
        0 => "Nothing needs you".to_string(),
        1 => "1 needs you".to_string(),
        n => format!("{n} need you"),
    }
}

/// Refreshes the tray's menu count line, tooltip and (on macOS) dock badge.
/// `main_window` is used only for the macOS dock badge, which is a window
/// (really application) property, not a tray property.
pub fn update_count<R: Runtime>(
    app: &AppHandle<R>,
    tray: &TrayIcon<R>,
    open_count: usize,
    #[allow(unused_variables)] main_window: Option<&tauri::WebviewWindow<R>>,
) {
    if let Ok(menu) = build_menu(app, open_count) {
        let _ = tray.set_menu(Some(menu));
    }
    let tooltip = if open_count == 0 {
        "Conductor".to_string()
    } else {
        format!("Conductor — {}", count_label(open_count))
    };
    let _ = tray.set_tooltip(Some(&tooltip));
    let _ = tray.set_title(if open_count == 0 {
        None
    } else {
        Some(open_count.to_string())
    });
    #[cfg(target_os = "macos")]
    {
        if let Some(window) = main_window {
            let badge = if open_count == 0 {
                None
            } else {
                Some(open_count as i64)
            };
            let _ = window.set_badge_count(badge);
        }
    }
}

/// Dispatches a tray menu click. `on_open` is called for "Open" (and also
/// implicitly by a left click on the icon, wired by the caller), `on_settings`
/// for "Settings", and the app exits on "Quit".
pub fn handle_menu_event<R: Runtime>(
    app: &AppHandle<R>,
    event: &MenuEvent,
    on_open: impl FnOnce(),
    on_settings: impl FnOnce(),
) {
    match event.id().as_ref() {
        MENU_OPEN => on_open(),
        MENU_SETTINGS => on_settings(),
        MENU_QUIT => app.exit(0),
        _ => {}
    }
}
