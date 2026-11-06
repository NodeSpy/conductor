fn main() {
    // Every app-defined command (everything in `tauri::generate_handler!` in
    // lib.rs) must get its own ACL permission, auto-generated as
    // `allow-<command-in-kebab-case>` / `deny-<command-in-kebab-case>` and
    // denied by default until a capability explicitly grants it.
    //
    // Without this list, Tauri only ACL-checks a *local* window's app
    // commands once the app defines an app manifest at all (see
    // tauri::webview::Webview::on_message: the check is skipped for local
    // origins when `!has_app_acl_manifest`); a *remote* origin's commands are
    // always checked regardless. Declaring every command here, rather than
    // relying on that, means every command — local or remote — is
    // deny-by-default and only reachable through an explicit capability grant
    // (see capabilities/default.json for the local setup/settings pages and
    // the runtime "remote-daemon" capability added in lib.rs::complete_setup
    // for the one command — ssh_sign — the daemon's page may call).
    tauri_build::try_build(
        tauri_build::Attributes::new().app_manifest(
            tauri_build::AppManifest::new().commands(&[
                "get_settings",
                "save_settings",
                "default_ssh_key",
                "complete_setup",
                "ssh_sign",
            ]),
        ),
    )
    .expect("failed to run tauri-build");
}
