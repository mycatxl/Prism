import { useEffect, useState, type ReactElement } from "react";
import { Navigate, useLocation } from "react-router-dom";
import { useAuthStore } from "./auth-store";
import { LoadingState } from "../../components/ui/QueryState";
import { fetchSession } from "../../lib/api-client";
import { ACCOUNT } from "../../lib/paths";
import { isAccountPath } from "../../lib/roles";

type RequireAuthProps = {
  children: ReactElement;
};

export function RequireAuth({ children }: RequireAuthProps) {
  const session = useAuthStore((state) => state.session);
  const location = useLocation();
  const [checked, setChecked] = useState(session.authenticated || !session.auth_required);

  useEffect(() => {
    // Nothing to probe when the store already knows the answer: the render path
    // below returns the children in that case, so calling setState here would be
    // a state update without a purpose.
    if (session.authenticated || !session.auth_required) {
      return;
    }
    let active = true;
    const controller = new AbortController();
    void fetchSession(controller.signal)
      .catch(() => {
        // Session probes are disposable; cancellation and probe failures fall back to the login path below.
      })
      .finally(() => {
        if (active) setChecked(true);
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [session.auth_required, session.authenticated]);

  if (session.authenticated || !session.auth_required) {
    if (session.must_enrol_totp && !isAccountPath(location.pathname)) {
      return <Navigate to={`${ACCOUNT}?enrol=1`} replace />;
    }
    return children;
  }

  if (!checked) {
    return (
      <main className="grid min-h-dvh place-items-center bg-paper">
        <LoadingState />
      </main>
    );
  }

  const next = `${location.pathname}${location.search}`;
  return <Navigate to={`/login?next=${encodeURIComponent(next)}`} replace />;
}
