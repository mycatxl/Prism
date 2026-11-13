import { initTheme } from "./lib/theme";

// The ground is set before the first paint so a dark console never flashes white.
initTheme();

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
