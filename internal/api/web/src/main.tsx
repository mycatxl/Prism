import "@fontsource/manrope/latin-400.css";
import "@fontsource/manrope/latin-500.css";
import "@fontsource/manrope/latin-600.css";
import "@fontsource/manrope/latin-700.css";
import "@fontsource/sora/latin-600.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { AppProviders } from "./app/providers";
import { AppRoutes } from "./app/routes";
import { ErrorBoundary } from "./components/ErrorBoundary";
import "./i18n";
import "./styles/theme.css";
import "./styles/workbench.css";
import "./styles/vibrancy.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <AppProviders>
      <BrowserRouter basename="/ui">
      <ErrorBoundary><AppRoutes /></ErrorBoundary>
      </BrowserRouter>
    </AppProviders>
  </StrictMode>,
);
