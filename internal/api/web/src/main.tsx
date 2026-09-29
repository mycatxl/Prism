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
import "./i18n";
import "./styles/design.css";

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