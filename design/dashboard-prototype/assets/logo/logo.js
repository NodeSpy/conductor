/* The one reference to the logo. This folder holds a complete Daylight logo kit (design/brand/logo-kits/<id>/,
 * currently fermata-bold, "Fermata, strengthened", the brand designer's recommended working mark) plus this file.
 * Every place that shows the logo (the top bar, the phone header, sign-in and enrolment, the favicon and the
 * home-screen icon) goes through UI.logo() and the links in index.html, which name the kit's standard file names.
 * Swapping the logo is a file drop: copy another kit's files over this folder. Nothing else changes.
 *   mark-inline.html  the inline mark for app chrome, coloured by tokens (--color-logo-ink, --color-accent-default)
 *   favicon.svg       the 16px cut (follows the browser's colour scheme); apple-touch-icon.png the home-screen icon
 * The iPhone suite checks the mark renders. */
window.LOGO = {
  inline: 'assets/logo/mark-inline.html',
  wordmark: 'conductor',
};
