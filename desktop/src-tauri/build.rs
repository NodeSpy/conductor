fn main() {
    // Every app-defined command (everything in `tauri::generate_handler!` in
    // lib.rs) must get its own ACL permission, auto-generated as
    // `allow-<command-in-kebab-case>` / `deny-<command-in-kebab-case>` and
    // denied by default until a capability explicitly grants it.
    //
    // Without this list, Tauri only ACL-checks a *local* window's app
    // commands once the app defines an app manifest at all (see
    // tauri::webview::Webview::on_message: the check is skipped for local
    // origins when `!has_app_acl_manifest`). Declaring every command here
    // means every command is deny-by-default and only reachable through an
    // explicit capability grant (capabilities/default.json grants all of
    // these to the local `main`/`settings` windows — there is no remote
    // origin or runtime capability anymore: the main window only ever shows
    // this app's own bundled UI, never a daemon page).
    tauri_build::try_build(
        tauri_build::Attributes::new().app_manifest(
            tauri_build::AppManifest::new().commands(&[
                "get_settings",
                "save_settings",
                "default_ssh_key",
                "complete_setup",
                "attention",
                "dismiss",
                "approval_view",
                "decide",
                "conversations",
                "messages",
                "send_message",
                "mark_read",
                "bots",
                "bot",
                "bot_proposals",
                "open_in_browser",
            ]),
        ),
    )
    .expect("failed to run tauri-build");
}
