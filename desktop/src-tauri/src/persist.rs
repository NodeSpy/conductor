//! Durable bits that aren't settings: the event stream's last cursor. Kept in
//! the app *data* dir (as opposed to settings, which live in the config dir)
//! since it's resumption state, not user configuration.

use std::fs;
use std::path::{Path, PathBuf};

fn cursor_path(data_dir: &Path) -> PathBuf {
    data_dir.join("cursor")
}

pub fn load_cursor(data_dir: &Path) -> Option<String> {
    let raw = fs::read_to_string(cursor_path(data_dir)).ok()?;
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        None
    } else {
        Some(trimmed.to_string())
    }
}

pub fn save_cursor(data_dir: &Path, cursor: &str) -> anyhow::Result<()> {
    fs::create_dir_all(data_dir)?;
    fs::write(cursor_path(data_dir), cursor)?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn roundtrip_cursor() {
        let dir = tempfile::tempdir().unwrap();
        assert_eq!(load_cursor(dir.path()), None);
        save_cursor(dir.path(), "epoch-123").unwrap();
        assert_eq!(load_cursor(dir.path()), Some("epoch-123".to_string()));
        save_cursor(dir.path(), "epoch-456").unwrap();
        assert_eq!(load_cursor(dir.path()), Some("epoch-456".to_string()));
    }

    #[test]
    fn empty_file_is_no_cursor() {
        let dir = tempfile::tempdir().unwrap();
        fs::create_dir_all(dir.path()).unwrap();
        fs::write(cursor_path(dir.path()), "  \n").unwrap();
        assert_eq!(load_cursor(dir.path()), None);
    }
}
