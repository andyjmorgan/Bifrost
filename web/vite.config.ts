import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const backend = 'http://localhost:8080'

// Everything the Go server owns is proxied in dev: the REST API, the MCP front door (mux and
// per-slug), the OAuth callback and the well-known documents. Client routes stay with Vite.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      '/api': backend,
      '/mcp': backend,
      '/oauth': backend,
      '/.well-known': backend,
      '/healthz': backend,
      '^/[a-z0-9]{1,12}/mcp': backend,
    },
  },
  build: { outDir: 'dist', emptyOutDir: true },
})
