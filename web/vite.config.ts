import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const backend = 'http://localhost:8080'

// Everything the Go server owns is proxied in dev: the REST API, the MCP front door (mux and
// per-entitlement slug), the OAuth callback and the well-known documents. Client routes stay with Vite.
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
      '^/[a-z0-9-]{1,24}/mcp': backend,
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks: {
          react: ['react', 'react-dom', 'react-router-dom'],
          radix: [
            '@radix-ui/react-dialog',
            '@radix-ui/react-dropdown-menu',
            '@radix-ui/react-select',
            '@radix-ui/react-switch',
            '@radix-ui/react-checkbox',
            '@radix-ui/react-label',
            '@radix-ui/react-slot',
          ],
          oidc: ['oidc-client-ts'],
          codemirror: ['@codemirror/state', '@codemirror/view', '@codemirror/autocomplete', '@codemirror/language', '@codemirror/commands', '@lezer/highlight'],
        },
      },
    },
  },
})
