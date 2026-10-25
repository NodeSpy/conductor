import { defineConfig } from 'vite'
import preact from '@preact/preset-vite'

// The SPA builds into the Go package's embed dir. `dist/` is committed so
// `go build`/`go install` never need Node (design §8). During local dev,
// `vite` proxies the API to a daemon running the dashboard on :7788.
export default defineConfig({
  plugins: [preact()],
  build: {
    outDir: '../internal/dashboard/dist',
    emptyOutDir: true,
    // Keep hashed assets under /assets so the Go server can cache them immutably.
    assetsDir: 'assets',
    sourcemap: false,
  },
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:7788',
        changeOrigin: true,
      },
    },
  },
})
