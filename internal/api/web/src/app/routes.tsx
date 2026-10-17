import { lazy, Suspense } from "react";
import { Navigate, Route, Routes } from "react-router-dom";
import { AppShell } from "../components/AppShell";
import { NotFoundPage } from "../components/NotFoundPage";
import { LoadingState } from "../components/ui/QueryState";
import { RequireAuth } from "../features/auth/RequireAuth";
import { useAuthStore } from "../features/auth/auth-store";
import { useI18n } from "../i18n";
import { isAdminRole, homePathFor } from "../lib/roles";
import { Page } from "./page";

const AccountSecurityPage = lazy(() => import("../features/auth/AccountSecurityPage").then((m) => ({ default: m.AccountSecurityPage })));
const LoginPage = lazy(() => import("../features/auth/LoginPage").then((m) => ({ default: m.LoginPage })));
const RegisterPage = lazy(() => import("../features/auth/RegisterPage").then((m) => ({ default: m.RegisterPage })));
const MeGuidePage = lazy(() => import("../features/me/GuidePage").then((m) => ({ default: m.GuidePage })));
const MeKeysPage = lazy(() => import("../features/me/KeysPage").then((m) => ({ default: m.KeysPage })));
const MeLeasesPage = lazy(() => import("../features/me/LeasesPage").then((m) => ({ default: m.LeasesPage })));
const MeDepositPage = lazy(() => import("../features/me/DepositPage").then((m) => ({ default: m.DepositPage })));
const MeOverviewPage = lazy(() => import("../features/me/OverviewPage").then((m) => ({ default: m.OverviewPage })));
const MePlansPage = lazy(() => import("../features/me/PlansPage").then((m) => ({ default: m.PlansPage })));

/** Landing route: the workbench for operators, the self-service surface for a tenant session. */
function HomeRedirect() {
  const session = useAuthStore((state) => state.session);
  return <Navigate to={homePathFor(session.user?.role)} replace />;
}

/**
 * The management route table, loaded only for an operator.
 *
 * A tenant session never requests this chunk, so the management paths are not in
 * the JavaScript it holds — see admin-routes.tsx. That is defence in depth, not
 * the control: the server refuses every management request from a tenant
 * whatever the client does (internal/api/rolegate.go).
 */
const LazyAdminRoutes = lazy(() =>
  import("./admin-routes").then((m) => ({ default: m.AdminRoutes })),
);

/**
 * The catch-all for a path outside the self-service surface.
 *
 * An operator gets the management table. A tenant gets the ordinary not-found
 * page, which is what the server answers for a management path they may not
 * use — a distinct refusal would confirm which paths exist.
 *
 * The loading placeholder must not be the not-found page: it would flash for
 * every first visit to a management route, and a slow chunk would read as a
 * missing page.
 */
function AdminSurface() {
  const session = useAuthStore((state) => state.session);
  const { t } = useI18n();
  // auth_required=false is a deployment with authentication switched off: every
  // surface is reachable, so the management table is loaded there too.
  const wantsAdmin = !session.auth_required || isAdminRole(session.user?.role);

  if (!wantsAdmin) return <NotFoundPage />;
  return (
    <Suspense fallback={<LoadingState label={t("载入页面")} className="py-24" />}>
      <LazyAdminRoutes />
    </Suspense>
  );
}

export function AppRoutes() {
  return (
    <Routes>
      <Route
        path="/login"
        element={
          <Page>
            <LoginPage />
          </Page>
        }
      />
      <Route
        path="/register"
        element={
          <Page>
            <RegisterPage />
          </Page>
        }
      />
      <Route
        element={
          <RequireAuth>
            <AppShell />
          </RequireAuth>
        }
      >
        <Route path="/" element={<HomeRedirect />} />
        <Route path="/me" element={<Page><MeOverviewPage /></Page>} />
        <Route path="/me/keys" element={<Page><MeKeysPage /></Page>} />
        <Route path="/me/guide" element={<Page><MeGuidePage /></Page>} />
        <Route path="/me/plans" element={<Page><MePlansPage /></Page>} />
        <Route path="/me/leases" element={<Page><MeLeasesPage /></Page>} />
        <Route path="/me/deposit" element={<Page><MeDepositPage /></Page>} />
        <Route path="/account-security" element={<Page><AccountSecurityPage /></Page>} />
        <Route path="*" element={<AdminSurface />} />
      </Route>
    </Routes>
  );
}
