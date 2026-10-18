import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { loadServerEnv, readServerConfig } from "./server/config.mjs";

export default defineConfig(({ mode }) => {
  const config = readServerConfig(
    loadServerEnv(process.cwd(), process.env, mode),
  );

  return {
    base: "/ui/",
    plugins: [react(), tailwindcss()],
    build: {
      chunkSizeWarningLimit: 1200,
      /*
       * Three cache tiers instead of one bundle. `dist/assets/*` is served
       * immutable, so a chunk only leaves the browser's cache when its own bytes
       * change: react, the router and the overlay primitives change when a
       * dependency is upgraded, while the entry chunk changes on every commit.
       *
       * Everything not named here is left to Rollup. That is deliberate — a
       * catch-all "vendor" bucket would pull `cmdk`, `zod` and
       * `i18n-iso-countries` into the entry, and each of those is only reachable
       * from a lazy route.
       */
      rollupOptions: {
        output: {
          manualChunks(id) {
            if (!id.includes("node_modules/")) return undefined;
            if (/node_modules\/(?:react|react-dom|scheduler)\//.test(id)) return "react";
            if (/node_modules\/react-router(?:-dom)?\//.test(id)) return "router";
            if (/node_modules\/@tanstack\//.test(id)) return "query";
            if (/node_modules\/(?:@radix-ui|@floating-ui)\//.test(id)) return "overlay";
            if (/node_modules\/(?:i18next|react-i18next)\//.test(id)) return "i18n";
            if (/node_modules\/lucide-react\//.test(id)) return "icons";
            return undefined;
          },
        },
      },
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