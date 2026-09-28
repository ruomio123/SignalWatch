import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";

// A standalone config deliberately avoids the development proxy and loadEnv.
export default defineConfig({
  envDir: false,
  plugins: [react()],
  cacheDir: fileURLToPath(new URL("../.cache/vite-agent/", import.meta.url)),
  server: {
    host: "127.0.0.1",
    port: 4173,
    strictPort: true,
  },
});
