/* Setup page: collects daemon URL, operator name and SSH key path, then hands
   them to the Rust side, which persists settings, grants the remote-daemon
   capability for ssh_sign, and navigates this window to the daemon. */
(function () {
  function invoke(cmd, args) {
    if (!window.__TAURI__ || !window.__TAURI__.core) {
      return Promise.reject(new Error("not running inside the Conductor app"));
    }
    return window.__TAURI__.core.invoke(cmd, args);
  }

  function setStatus(msg, kind) {
    var el = document.getElementById("status");
    el.textContent = msg || "";
    el.className = kind || "";
  }

  document.addEventListener("DOMContentLoaded", function () {
    var form = document.getElementById("setup-form");
    var daemonUrl = document.getElementById("daemon-url");
    var operator = document.getElementById("operator");
    var sshKey = document.getElementById("ssh-key");
    var saveBtn = document.getElementById("save-btn");

    invoke("get_settings")
      .then(function (s) {
        if (s) {
          daemonUrl.value = s.daemon_url || "";
          operator.value = s.operator || "";
          sshKey.value = s.ssh_public_key_path || "";
        }
      })
      .catch(function () {
        /* first run, or not inside the app (e.g. previewed in a browser) */
      });

    if (!sshKey.value) {
      invoke("default_ssh_key")
        .then(function (path) {
          if (path) sshKey.value = path;
        })
        .catch(function () {});
    }

    form.addEventListener("submit", function (ev) {
      ev.preventDefault();
      saveBtn.disabled = true;
      setStatus("Connecting…", "");
      var settings = {
        daemon_url: daemonUrl.value.trim(),
        operator: operator.value.trim().toLowerCase(), /* conductor operator names are lowercase */
        ssh_public_key_path: sshKey.value.trim(),
      };
      invoke("save_settings", { settings: settings })
        .then(function () {
          return invoke("complete_setup");
        })
        .then(function () {
          setStatus("Connected. Opening the dashboard…", "ok");
        })
        .catch(function (err) {
          saveBtn.disabled = false;
          setStatus(String(err && err.message ? err.message : err), "error");
        });
    });
  });
})();
