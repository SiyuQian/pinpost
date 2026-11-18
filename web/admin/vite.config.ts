import path from "node:path"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vite"

// The Go server mounts this SPA under /admin/ and owns /v1; in dev we proxy
// /v1 to it so the session cookie stays same-origin either way.
export default defineConfig({
  base: "/admin/",
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": path.resolve(import.meta.dirname, "./src") } },
  server: {
    port: 5473,
    proxy: { "/v1": { target: process.env.PINPOST_API_URL ?? "http://localhost:8787", changeOrigin: true } },
  },
  build: { outDir: "dist", emptyOutDir: true },
})
