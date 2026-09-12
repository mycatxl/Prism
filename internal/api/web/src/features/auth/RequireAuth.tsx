import { useEffect, useState, type ReactElement } from "react";
import { Navigate, useLocation } from "react-router-dom";
import { useAuthStore } from "./auth-store";
import { apiRequest } from "../../lib/api-client";
import { QueryState } from "../../components/ui/QueryState";

type RequireAuthProps = {
  children: ReactElement;
};

export function RequireAuth({ children }: RequireAuthProps) {
  const token = useAuthStore((state) => state.token);
  const location = useLocation();
  const [checked, setChecked] = useState(Boolean(token));
  const [anonymousAllowed, setAnonymousAllowed] = useState(false);

  useEffect(() => {
    if (token) {
      setAnonymousAllowed(false);
      setChecked(true);
      return;
    }

    let active = true;
    const controller = new AbortController();

    const checkAuthMode = async () => {
      try {
        await apiRequest("/api/v1/system/info", {
          auth: false,
          signal: controller.signal,
        });
        if (!active) {
          return;
        }
        // /api/v1/system/info returns 200 only when admin auth is disabled.
        setAnonymousAllowed(true);
      } catch {
        if (!active) {
          return;
        }
        setAnonymousAllowed(false);
      } finally {
        if (active) {
          setChecked(true);
        }
      }
    };

    void checkAuthMode();

    return () => {
      active = false;
      controller.abort();
    };
  }, [token]);

  if (token || anonymousAllowed) {
    return children;
  }

  if (!checked) {
    return <main className="login-layout"><QueryState loading /></main>;
  }

  const next = `${location.pathname}${location.search}`;
  return <Navigate to={`/login?next=${encodeURIComponent(next)}`} replace />;
}
