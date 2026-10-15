import type { ReactElement } from "react";
import { Navigate, useLocation } from "react-router-dom";
import { homePathFor } from "../../lib/roles";
import { useAuthStore } from "./auth-store";

/**
 * Role boundary for the management surfaces.
 *
 * This is convenience, not the control: every request behind these pages is
 * refused by `requireAdmin` on the server whatever the client does. The guard
 * exists so a tenant who follows an old link lands on their own surface instead
 * of a page whose every query would come back 403.
 */
export function RequireAdmin({ children }: { children: ReactElement }) {
  const session = useAuthStore((state) => state.session);
  const location = useLocation();

  if (!session.auth_required || session.user?.role === "admin") {
    return children;
  }

  return <Navigate to={homePathFor(session.user?.role)} replace state={{ from: location.pathname }} />;
}
