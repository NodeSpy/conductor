// Set the theme before first paint: an explicit choice wins, else the OS.
// External (not inline) so the dashboard's strict CSP (script-src 'self') covers
// it without a brittle hash (design §6.4).
;(function () {
  try {
    var t = localStorage.getItem('theme')
    if (t === 'light' || t === 'dark') document.documentElement.setAttribute('data-theme', t)
  } catch (e) {}
})()
