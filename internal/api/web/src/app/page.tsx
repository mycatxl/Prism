import { Suspense, type ReactNode } from "react";
import { useLocation } from "react-router-dom";
import { LoadingState } from "../components/ui/QueryState";

/**
 * Route boundary.
 *
 * The `key` on the fallback replaces the whole region when the destination
 * changes, so a slow route never shows the previous page's content under a new
 * title. Focus movement after navigation is handled once, in `AppShell`.
 */
export function Page({ children }: { children: ReactNode }) {
  const location = useLocation();
  return (
    <Suspense
      key={location.pathname}
      fallback={<LoadingState label="载入页面" className="py-24" />}
    >
      {children}
    </Suspense>
  );
}
