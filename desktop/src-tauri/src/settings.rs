//! Settings persisted in the app config dir as JSON: daemon URL, operator name
//! and the SSH public key path used for the background sign-in
//! (`Client::sign_in`) and the SSH step-up a decision needs
//! (`Client::decide`).

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

    fn tempdir() -> tempfile::TempDir {
        tempfile::tempdir().unwrap()
    }
}
