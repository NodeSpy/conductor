// Prevents an additional console window from appearing on Windows (no-op on
// Linux/macOS, the only targets this app actually ships for).
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    conductor_desktop_lib::run();
}
