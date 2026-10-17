/* The one reference to the logo. Every place that shows it (top bar / phone header, sign-in and enrolment,
 * the favicon) goes through UI.logo(), so swapping the logo is a file drop into assets/logo/:
 *   mark.svg    a <symbol id="mark">, themed with var(--logo-ink) / var(--logo-accent)
 *   lockup.svg  a <symbol id="lockup"> (set lockup below to use it instead of mark + wordmark text)
 *   favicon.svg the tab icon
 */
window.LOGO = {
  mark: 'assets/logo/mark.svg#mark',
  lockup: null, // e.g. 'assets/logo/lockup.svg#lockup' once a finished lockup exists
  lockupRatio: 132 / 24,
  wordmark: 'conductor',
  favicon: 'assets/logo/favicon.svg',
};
