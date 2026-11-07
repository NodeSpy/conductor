import { defineConfig } from 'vite'
import preact from '@preact/preset-vite'

// The desktop app's local UI. Built into the Tauri `frontendDist`
// (`../dist`, see tauri.conf.json); `tauri.conf.json`'s `beforeBuildCommand`
// runs `npm run build` here. There is no API proxy: in the real app every
// daemon call goes through a typed Tauri command (desktop/src-tauri/src/
// client.rs + lib.rs), never a browser fetch. `?mock` mode (src/api/mock.ts)
// stands in for `invoke` during `npm run dev`/`preview` so screens can be
// built and screenshotted without Tauri or a daemon.
export default defineConfig({
  plugins: [preact()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    assetsDir: 'assets',
    sourcemap: false,
  },
})
