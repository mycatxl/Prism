import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { loadServerEnv, readServerConfig } from "./server/config.mjs";

export default defineConfig(({ mode }) => {
  const config = readServerConfig(
    loadServerEnv(process.cwd(), process.env, mode),
  );

  return {
    base: "/ui/",
    plugins: [react()],
    build: {
      chunkSizeWarningLimit: 1200,
    },
    server: {
      host: config.host,
      port: config.port,
      strictPort: true,
      proxy: {
        "/api": {
          target: config.apiTarget,
          changeOrigin: true,
        },
        "/healthz": {
          target: config.apiTarget,
          changeOrigin: true,
        },
      },
    },
    preview: {
      host: config.host,
      port: config.port,
      strictPort: true,
    },
  };
});
