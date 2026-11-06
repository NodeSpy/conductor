//! Native SSH signing: shells out to `ssh-keygen -Y sign`, which reads the
//! message on stdin and (with no file arguments) writes the armored SSHSIG to
//! stdout. The private key never needs to touch this process: `-f` points at
//! the *public* key, and `ssh-keygen` reaches the private half through
//! `ssh-agent` when the public key's matching identity is loaded there (it
//! also works directly if `-f` names a private key file, but we always point
//! at the configured `.pub` file per the design).
//!
//! Only two namespaces are ever allowed to be signed: the dashboard's login
//! and step-up challenges. Everything else (enrollment, bot approvals, git
//! push signing, or a typo) is rejected before a process is ever spawned.

use std::process::Stdio;

use thiserror::Error;
use tokio::io::AsyncWriteExt;
use tokio::process::Command;

/// The only namespaces `ssh_sign` will ever sign. These match
/// `internal/dashboard/auth.LoginNamespace` / `StepupNamespace` in the
/// daemon; keep them in sync if those ever change.
pub const LOGIN_NAMESPACE: &str = "conductor-dashboard-login";
pub const STEPUP_NAMESPACE: &str = "conductor-dashboard-stepup";

const ALLOWED_NAMESPACES: &[&str] = &[LOGIN_NAMESPACE, STEPUP_NAMESPACE];

#[derive(Debug, Error)]
pub enum SignError {
    #[error("namespace {0:?} is not one this app will sign")]
    NamespaceNotAllowed(String),
    #[error("could not run ssh-keygen: {0}")]
    Spawn(#[source] std::io::Error),
    #[error("could not write the challenge to ssh-keygen: {0}")]
    Write(#[source] std::io::Error),
    #[error("ssh-keygen failed: {0}")]
    KeygenFailed(String),
    #[error("ssh-keygen produced no signature")]
    Empty,
}

pub fn namespace_allowed(namespace: &str) -> bool {
    ALLOWED_NAMESPACES.contains(&namespace)
}

/// Signs `message` in `namespace` with the key at `pubkey_path`, returning the
/// armored SSHSIG text. Rejects any namespace outside the allowlist before
/// spawning a process.
pub async fn ssh_sign(
    pubkey_path: &str,
    namespace: &str,
    message: &str,
) -> Result<String, SignError> {
    if !namespace_allowed(namespace) {
        return Err(SignError::NamespaceNotAllowed(namespace.to_string()));
    }

    let mut child = Command::new("ssh-keygen")
        .arg("-Y")
        .arg("sign")
        .arg("-f")
        .arg(pubkey_path)
        .arg("-n")
        .arg(namespace)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true)
        .spawn()
        .map_err(SignError::Spawn)?;

    {
        let mut stdin = child.stdin.take().expect("piped stdin");
        stdin
            .write_all(message.as_bytes())
            .await
            .map_err(SignError::Write)?;
        // Drop closes stdin so ssh-keygen sees EOF.
    }

    let output = child
        .wait_with_output()
        .await
        .map_err(SignError::Spawn)?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr).trim().to_string();
        return Err(SignError::KeygenFailed(if stderr.is_empty() {
            format!("exit status {:?}", output.status.code())
        } else {
            stderr
        }));
    }

    let sig = String::from_utf8_lossy(&output.stdout).trim().to_string();
    if sig.is_empty() || !sig.contains("BEGIN SSH SIGNATURE") {
        return Err(SignError::Empty);
    }
    Ok(sig)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn allowlist_accepts_only_dashboard_namespaces() {
        assert!(namespace_allowed(LOGIN_NAMESPACE));
        assert!(namespace_allowed(STEPUP_NAMESPACE));
        assert!(!namespace_allowed("conductor-dashboard-enroll"));
        assert!(!namespace_allowed("git"));
        assert!(!namespace_allowed(""));
        assert!(!namespace_allowed("conductor-dashboard-login-extra"));
    }

    #[tokio::test]
    async fn rejects_bad_namespace_before_spawning() {
        let err = ssh_sign("/nonexistent/key.pub", "not-a-real-namespace", "hello")
            .await
            .unwrap_err();
        assert!(matches!(err, SignError::NamespaceNotAllowed(_)));
    }
}
