import { initTheme } from "./lib/theme";

// The ground is set before the first paint so a dark console never flashes white.
initTheme();

/*
 * One family, two voices (see `--font-sans` / `--font-mono` in design.css).
 * Manrope used to be imported here and matched no rule in the stylesheet: four
 * weights of `@font-face` and eight files shipped for a face nothing asked for.
 */
import "@fontsource/ibm-plex-sans/latin-400.css";
import "@fontsource/ibm-plex-sans/latin-500.css";
import "@fontsource/ibm-plex-sans/latin-600.css";
import "@fontsource/ibm-plex-mono/latin-400.css";
import "@fontsource/ibm-plex-mono/latin-500.css";
import "@fontsource/ibm-plex-mono/latin-600.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { AppProviders } from "./app/providers";
import { AppRoutes } from "./app/routes";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { primeLocale } from "./i18n";
import "./styles/design.css";

/*
 * English is a chunk (see `i18n/english.ts`), so the boot waits for it when the
 * session speaks English and resolves immediately when it does not. Rendering
 * first and swapping later would show Chinese keys for one frame, which reads as
 * a broken build rather than a loading state.
 */
function mount() {
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <AppProviders>
        <BrowserRouter basename="/ui">
          <ErrorBoundary>
            <AppRoutes />
          </ErrorBoundary>
        </BrowserRouter>
      </AppProviders>
    </StrictMode>,
  );
}

void primeLocale().then(mount, mount);
