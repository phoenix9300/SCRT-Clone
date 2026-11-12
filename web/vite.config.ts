import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import path from "node:path";

// The production bundle is emitted into the Go module so it can be embedded
// into the single `hawser` binary with go:embed.
export default defineConfig({
  plugins: [react()],
  base: "./",
  build: {
    outDir: path.resolve(import.meta.dirname, "../internal/webui/dist/app"),
    emptyOutDir: true,
    target: "es2022",
    chunkSizeWarningLimit: 2500,
  },
  server: {
    port: 5173,
    proxy: {
      "/ws": { target: "ws://127.0.0.1:7422", ws: true },
      "/api": { target: "http://127.0.0.1:7422" },
    },
  },
  test: {
    include: ["src/**/*.test.ts", "tests/**/*.test.ts"],
    environment: "node",
  },
});
