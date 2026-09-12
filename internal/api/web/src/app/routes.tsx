import { lazy, Suspense, useEffect, type ReactNode } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { AppShell } from "../components/AppShell";
import { QueryState } from "../components/ui/QueryState";
import { RequireAuth } from "../features/auth/RequireAuth";
import { NotFoundPage } from "../components/NotFoundPage";

const LoginPage = lazy(() => import("../features/auth/LoginPage").then(m => ({ default: m.LoginPage })));
const WorkbenchPage = lazy(() => import("../features/dashboard/WorkbenchPage").then(m => ({ default: m.WorkbenchPage })));
const EndpointsPage = lazy(() => import("../features/endpoints/EndpointsPage").then(m => ({ default: m.EndpointsPage })));
const GeoIPPage = lazy(() => import("../features/geoip/GeoIPPage").then(m => ({ default: m.GeoIPPage })));
const NodesPage = lazy(() => import("../features/nodes/NodesPage").then(m => ({ default: m.NodesPage })));
const PlatformDetailPage = lazy(() => import("../features/platforms/PlatformDetailPage").then(m => ({ default: m.PlatformDetailPage })));
const PlatformPage = lazy(() => import("../features/platforms/PlatformPage").then(m => ({ default: m.PlatformPage })));
const RequestLogsPage = lazy(() => import("../features/requestLogs/RequestLogsPage").then(m => ({ default: m.RequestLogsPage })));
const RulesPage = lazy(() => import("../features/rules/RulesPage").then(m => ({ default: m.RulesPage })));
const SubscriptionPage = lazy(() => import("../features/subscriptions/SubscriptionPage").then(m => ({ default: m.SubscriptionPage })));
const SystemConfigPage = lazy(() => import("../features/systemConfig/SystemConfigPage").then(m => ({ default: m.SystemConfigPage })));

function FocusContent({ children }: { children: ReactNode }) {
  useEffect(() => {
    const heading = document.querySelector<HTMLElement>(".content h1,.content h2");
    heading?.setAttribute("tabindex","-1");
    heading?.focus({ preventScroll:true });
  }, []);
  return children;
}
function Page({ children }: { children: ReactNode }) {
  const location = useLocation();
  return <Suspense fallback={<QueryState loading />}><FocusContent key={location.pathname}>{children}</FocusContent></Suspense>;
}

function LegacyQualityRedirect() {
  const location = useLocation();
  const previous = new URLSearchParams(location.search);
  const next = new URLSearchParams({ view: "exits" });
  for (const [oldKey, newKey] of [["ip", "quality_ip"], ["q", "quality_q"], ["page", "quality_page"]]) {
    const value = previous.get(oldKey);
    if (value) next.set(newKey, value);
  }
  return <Navigate to={"/nodes?" + next.toString()} replace />;
}

export function AppRoutes() {
  return <Routes>
    <Route path="/login" element={<Page><LoginPage /></Page>} />
    <Route element={<RequireAuth><AppShell /></RequireAuth>}>
      <Route path="/" element={<Navigate to="/dashboard" replace />} />
      <Route path="/dashboard" element={<Page><WorkbenchPage /></Page>} />
      <Route path="/platforms" element={<Page><PlatformPage /></Page>} />
      <Route path="/platforms/:platformId" element={<Page><PlatformDetailPage /></Page>} />
      <Route path="/subscriptions" element={<Page><SubscriptionPage /></Page>} />
      <Route path="/nodes" element={<Page><NodesPage /></Page>} />
      <Route path="/quality" element={<LegacyQualityRedirect />} />
      <Route path="/endpoints" element={<Page><EndpointsPage /></Page>} />
      <Route path="/rules" element={<Page><RulesPage /></Page>} />
      <Route path="/request-logs" element={<Page><RequestLogsPage /></Page>} />
      <Route path="/resources" element={<Page><GeoIPPage /></Page>} />
      <Route path="/system-config" element={<Page><SystemConfigPage /></Page>} />
      <Route path="*" element={<NotFoundPage />} />
    </Route>
  </Routes>;
}
