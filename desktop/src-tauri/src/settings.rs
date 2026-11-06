//! Settings persisted in the app config dir as JSON: daemon URL, operator name
//! and the SSH public key path used both for the background sign-in and for
//! `ssh_sign` confirmations.

use std::fs;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct Settings {
    pub daemon_url: String,
    pub operator: String,
    pub ssh_public_key_path: String,
}

impl Default for Settings {
    fn default() -> Self {
        Settings {
            daemon_url: String::new(),
            operator: String::new(),
            ssh_public_key_path: String::new(),
        }
    }
}

impl Settings {
    pub fn is_configured(&self) -> bool {
        !self.daemon_url.trim().is_empty()
            && !self.operator.trim().is_empty()
            && !self.ssh_public_key_path.trim().is_empty()
    }

    /// The daemon origin ("scheme://host[:port]"), used both for navigation
    /// and as the `remote.urls` scope of the runtime ssh_sign capability.
    pub fn daemon_origin(&self) -> Option<String> {
        let url = url::Url::parse(self.daemon_url.trim()).ok()?;
        Some(origin_string(&url))
    }

    /// Whether `url` is the currently configured daemon origin. This is the
    /// defense-in-depth check `ssh_sign` runs against the calling webview's
    /// *current* URL on every call, not just once at setup time.
    ///
    /// It matters because `tauri::ipc::RuntimeAuthority::add_capability`
    /// (called from `complete_setup` whenever the daemon URL changes) only
    /// ever *appends* a new remote-origin grant for "ssh_sign"; it does not
    /// revoke the previous origin's grant for the lifetime of the running
    /// app. Without this check, re-pointing the app at a new daemon would
    /// leave the old origin able to invoke `ssh_sign` for as long as the app
    /// stays open (e.g. if something could make the window navigate back
    /// there). This check makes the *current* settings the only origin that
    /// can ever succeed, regardless of how many stale capability grants have
    /// accumulated.
    pub fn allows_origin(&self, url: &url::Url) -> bool {
        match self.daemon_origin() {
            Some(expected) => expected == origin_string(url),
            None => false,
        }
    }
}

/// "scheme://host[:port]" for a URL, dropping path/query/fragment/credentials
/// — what both `daemon_origin` and `allows_origin` compare on.
fn origin_string(url: &url::Url) -> String {
    let scheme = url.scheme();
    let host = url.host_str().unwrap_or_default();
    match url.port() {
        Some(p) => format!("{scheme}://{host}:{p}"),
        None => format!("{scheme}://{host}"),
    }
}

fn settings_path(config_dir: &Path) -> PathBuf {
    config_dir.join("settings.json")
}

pub fn load(config_dir: &Path) -> Settings {
    let path = settings_path(config_dir);
    match fs::read_to_string(&path) {
        Ok(raw) => serde_json::from_str(&raw).unwrap_or_default(),
        Err(_) => Settings::default(),
    }
}

pub fn save(config_dir: &Path, settings: &Settings) -> anyhow::Result<()> {
    fs::create_dir_all(config_dir)?;
    let path = settings_path(config_dir);
    let raw = serde_json::to_string_pretty(settings)?;
    fs::write(path, raw)?;
    Ok(())
}

/// First existing default SSH public key, checked in order.
pub fn default_ssh_key() -> Option<String> {
    let home = dirs::home_dir()?;
    for name in ["id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub"] {
        let p = home.join(".ssh").join(name);
        if p.is_file() {
            return Some(p.to_string_lossy().to_string());
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn roundtrip() {
        let dir = tempdir();
        let s = Settings {
            daemon_url: "https://box.tailnet.ts.net:7788".into(),
            operator: "dan".into(),
            ssh_public_key_path: "/home/dan/.ssh/id_ed25519.pub".into(),
        };
        save(dir.path(), &s).unwrap();
        let loaded = load(dir.path());
        assert_eq!(s, loaded);
    }

    #[test]
    fn missing_file_is_default() {
        let dir = tempdir();
        let loaded = load(dir.path());
        assert_eq!(loaded, Settings::default());
        assert!(!loaded.is_configured());
    }

    #[test]
    fn origin_strips_path() {
        let s = Settings {
            daemon_url: "http://localhost:7788/".into(),
            operator: "dan".into(),
            ssh_public_key_path: "x".into(),
        };
        assert_eq!(s.daemon_origin().unwrap(), "http://localhost:7788");
    }

    fn tempdir() -> tempfile::TempDir {
        tempfile::tempdir().unwrap()
    }

    fn settings_for(daemon_url: &str) -> Settings {
        Settings {
            daemon_url: daemon_url.into(),
            operator: "dan".into(),
            ssh_public_key_path: "x".into(),
        }
    }

    #[test]
    fn allows_origin_matches_exact_scheme_host_port() {
        let s = settings_for("https://box.tailnet.ts.net:7788/dashboard");
        let url = url::Url::parse("https://box.tailnet.ts.net:7788/#/chat").unwrap();
        assert!(s.allows_origin(&url));
    }

    #[test]
    fn allows_origin_rejects_different_host() {
        let s = settings_for("https://box.tailnet.ts.net:7788");
        let evil = url::Url::parse("https://attacker.example:7788").unwrap();
        assert!(!s.allows_origin(&evil));
    }

    #[test]
    fn allows_origin_rejects_different_port() {
        let s = settings_for("http://localhost:7788");
        let other_port = url::Url::parse("http://localhost:9999").unwrap();
        assert!(!s.allows_origin(&other_port));
    }

    #[test]
    fn allows_origin_rejects_different_scheme() {
        let s = settings_for("https://box.tailnet.ts.net:7788");
        let downgraded = url::Url::parse("http://box.tailnet.ts.net:7788").unwrap();
        assert!(!s.allows_origin(&downgraded));
    }

    #[test]
    fn allows_origin_rejects_when_unconfigured() {
        let s = Settings::default();
        let url = url::Url::parse("http://localhost:7788").unwrap();
        assert!(!s.allows_origin(&url));
    }

    #[test]
    fn allows_origin_is_insensitive_to_path_and_query() {
        let s = settings_for("http://localhost:7788");
        let url = url::Url::parse("http://localhost:7788/some/path?x=1#/frag").unwrap();
        assert!(s.allows_origin(&url));
    }

    #[test]
    fn allows_origin_rejects_stale_origin_after_reconfigure() {
        // Simulates the scenario the security fix targets: the operator
        // repoints the app at a new daemon, but a stale runtime capability
        // for the old origin could still be present (add_capability only
        // appends). The current settings are the only thing this check
        // trusts.
        let old_url = url::Url::parse("https://old-box.ts.net:7788").unwrap();
        let s = settings_for("https://new-box.ts.net:7788");
        assert!(!s.allows_origin(&old_url));
    }
}
