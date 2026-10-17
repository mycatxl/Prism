import type { ReactElement } from "react";
import { NotFoundPage } from "../../components/NotFoundPage";
import { useAuthStore } from "./auth-store";

/**
 * Role boundary for the management surfaces.
 *
 * This is convenience, not the control: every request behind these pages is
 * refused by `requireAdmin` on the server whatever the client does.
 *
 * A tenant session sees the ordinary not-found page, not a refusal. The server
 * answers 404 for a management route a tenant may not use, exactly as it does
 * for a path that was never routed, and this guard keeps the console telling
 * the same story: a distinct "no permission" screen would confirm which paths
 * exist, which is the one thing a prober is after.
 */
export function RequireAdmin({ children }: { children: ReactElement }) {
  const session = useAuthStore((state) => state.session);

  if (!session.auth_required || session.user?.role === "admin") {
    return children;
  }

  return <NotFoundPage />;
}
