import { lazy, Suspense } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { AppShell } from "../components/AppShell";
import { NotFoundPage } from "../components/NotFoundPage";
import { LoadingState } from "../components/ui/QueryState";
import { RequireAuth } from "../features/auth/RequireAuth";
import { useAuthStore } from "../features/auth/auth-store";
import { useI18n } from "../i18n";
import { ACCOUNT, OPS_ROOT, legacyTargetFor, mePaths } from "../lib/paths";
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
 * The operator route table, loaded only for an operator.
 *
 * A tenant session never requests this chunk, so the operator paths are not in
 * the JavaScript it holds — see admin-routes.tsx. That is defence in depth, not
 * the control: the server refuses every management request from a tenant
 * whatever the client does (internal/api/rolegate.go).
 */
const LazyAdminRoutes = lazy(() =>
  import("./admin-routes").then((m) => ({ default: m.AdminRoutes })),
);

/**
 * The operator surface, mounted at `OPS_ROOT` (`/ops/*`).
 *
 * An operator gets the management table. A tenant gets the ordinary not-found
 * page, which is what the server answers for a management path they may not use
 * — a distinct refusal would confirm which paths exist.
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

/**
 * The catch-all: an address outside both namespaces.
 *
 * An old address is moved to where its page lives now — the table is
 * `legacyRedirects` in src/lib/paths.ts, and the part of the path that names a
 * detail (an id) and the search string are carried across with it. Anything the
 * table does not name is the operator surface, which answers a tenant with the
 * ordinary not-found page, and `/ops/*` handles the namespace itself above.
 */
function FallbackRoute() {
  const { pathname, search } = useLocation();
  const target = legacyTargetFor(pathname);
  if (target) return <Navigate to={`${target}${search}`} replace />;
  return <AdminSurface />;
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
        <Route path={mePaths.overview} element={<Page><MeOverviewPage /></Page>} />
        <Route path={mePaths.keys} element={<Page><MeKeysPage /></Page>} />
        <Route path={mePaths.guide} element={<Page><MeGuidePage /></Page>} />
        <Route path={mePaths.plans} element={<Page><MePlansPage /></Page>} />
        <Route path={mePaths.leases} element={<Page><MeLeasesPage /></Page>} />
        <Route path={mePaths.deposit} element={<Page><MeDepositPage /></Page>} />
        <Route path={ACCOUNT} element={<Page><AccountSecurityPage /></Page>} />
        <Route path={`${OPS_ROOT}/*`} element={<AdminSurface />} />
        <Route path="*" element={<FallbackRoute />} />
      </Route>
    </Routes>
  );
}
