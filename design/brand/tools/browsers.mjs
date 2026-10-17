// The browser matrix every visual check runs in. WebKit is the iPhone engine (the paseo
// in-app browser and Safari); Chromium with force-dark stands in for browsers and WebViews
// that auto-darken pages which don't declare a colour scheme.
//
// Needs Playwright with Chromium and WebKit installed. Point PLAYWRIGHT_PATH at the module
// and PLAYWRIGHT_BROWSERS_PATH at the browsers if they aren't in the default places.
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const pw = require(process.env.PLAYWRIGHT_PATH || 'playwright');

export const ENGINES = [
  { id: 'webkit-iphone', type: 'webkit', device: 'iPhone 15' },
  { id: 'webkit-desktop', type: 'webkit', viewport: { width: 1440, height: 900 } },
  { id: 'chromium-desktop', type: 'chromium', viewport: { width: 1440, height: 900 } },
  // Real auto-darkening only happens when the OS is dark; the test flag would force it in
  // light mode too, which no browser does, so this engine only runs with the OS dark.
  { id: 'chromium-phone-forcedark', type: 'chromium', viewport: { width: 390, height: 844 }, args: ['--blink-settings=forceDarkModeEnabled=true'], osModes: ['dark'] },
];

const launched = new Map();
export async function context(engine, colorScheme) {
  const key = engine.type + (engine.args ?? []).join(' ');
  if (!launched.has(key)) launched.set(key, await pw[engine.type].launch({ args: engine.args }));
  const browser = launched.get(key);
  const base = engine.device ? { ...pw.devices[engine.device] } : { viewport: engine.viewport, deviceScaleFactor: 1 };
  return browser.newContext({ ...base, colorScheme });
}
export async function closeAll() {
  for (const b of launched.values()) await b.close();
  launched.clear();
}
export function available(only) {
  return ENGINES.filter((e) => !only || only.split(',').includes(e.id));
}
