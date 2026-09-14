import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
export default defineConfig(({ mode }) => {
  const target =
    process.env.DEV_API_TARGET ||
    loadEnv(mode, process.cwd(), "DEV_").DEV_API_TARGET ||
    "http://127.0.0.1:8080";
  return {
    plugins: [react()],
    build: { outDir: "dist", emptyOutDir: true, manifest: true },
    server: {
      host: "127.0.0.1",
      port: 5173,
      strictPort: true,
      proxy: {
        "^/api(?:/|$)": target,
        "^/(?:healthz|readyz)$": target,
      },
    },
  };
});
