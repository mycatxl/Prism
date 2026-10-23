import { Suspense, useEffect, useRef, type ReactNode } from "react";
import { useLocation } from "react-router-dom";
import { LoadingState } from "../components/ui/QueryState";
import { useI18n } from "../i18n";

/**
 * Route boundary.
 *
 * A route is often a lazy chunk. Focusing from the shell on pathname change can
 * therefore land on the previous page or on the loading placeholder. This
 * boundary owns the focus handoff and waits for the real page heading to exist.
 */
export function Page({ children }: { children: ReactNode }) {
  const location = useLocation();
  const { t } = useI18n();
  const rootRef = useRef<HTMLElement>(null);

  useEffect(() => {
    const root = rootRef.current;
    if (!root) return;

    const focusHeading = () => {
      const heading = root.querySelector<HTMLElement>("h1");
      if (!heading) return false;
      heading.tabIndex = -1;
      heading.focus({ preventScroll: true });
      return true;
    };

    if (focusHeading()) return;

    const observer = new MutationObserver(() => {
      if (focusHeading()) observer.disconnect();
    });
    observer.observe(root, { childList: true, subtree: true });
    return () => observer.disconnect();
  }, [location.pathname]);

  return (
    <section key={location.pathname} ref={rootRef}>
      <Suspense
        key={location.pathname}
        fallback={<LoadingState label={t("载入页面")} className="py-24" />}
      >
        {children}
      </Suspense>
    </section>
  );
}
