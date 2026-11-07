// Set the theme, density and motion before first paint: an explicit choice
// wins, else the default (OS for theme, comfortable/standard otherwise).
// External (not inline) so the dashboard's strict CSP (script-src 'self')
// covers it without a brittle hash (design §6.4).
;(function () {
  try {
    var t = localStorage.getItem('theme')
    if (t === 'light' || t === 'dark') document.documentElement.setAttribute('data-theme', t)
    var d = localStorage.getItem('density')
    if (d === 'compact') document.documentElement.setAttribute('data-density', d)
    var m = localStorage.getItem('motion')
    if (m === 'reduced' || m === 'standard') document.documentElement.setAttribute('data-motion', m)
  } catch (e) {}
})()
