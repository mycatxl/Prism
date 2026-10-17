import { lazy } from "react";
import { Outlet, useRoutes, type RouteObject } from "react-router-dom";
import { RequireAdmin } from "../features/auth/RequireAdmin";
import { NotFoundPage } from "../components/NotFoundPage";
import { LegacyQualityRedirect } from "./legacy-quality-redirect";
import { Page } from "./page";

/**
 * Management routes.
 *
 * This module is imported dynamically, and only for a session whose role is
 * admin: a tenant's browser never downloads the route table below, so these
 * paths do not appear in the JavaScript it holds. That is defence in depth, not
 * the control — the server refuses every management request from a tenant
 * whatever the client does (internal/api/rolegate.go).
 *
 * Every page stays behind its own dynamic import, so opening the console as an
 * operator still loads only the page being visited.
 */
const AdminInvitesPage = lazy(() => import("../features/admin/InvitesPage").then((m) => ({ default: m.InvitesPage })));
const AdminPlansPage = lazy(() => import("../features/admin/PlansPage").then((m) => ({ default: m.PlansPage })));
const AdminSettingsPage = lazy(() => import("../features/admin/SettingsPage").then((m) => ({ default: m.SettingsPage })));
const AdminRedeemCodesPage = lazy(() => import("../features/admin/RedeemCodesPage").then((m) => ({ default: m.RedeemCodesPage })));
const AdminUsagePage = lazy(() => import("../features/admin/UsagePage").then((m) => ({ default: m.UsagePage })));
const AdminUserDetailPage = lazy(() => import("../features/admin/UserDetailPage").then((m) => ({ default: m.UserDetailPage })));
const AdminUsersPage = lazy(() => import("../features/admin/UsersPage").then((m) => ({ default: m.UsersPage })));
const WorkbenchPage = lazy(() => import("../features/dashboard/WorkbenchPage").then((m) => ({ default: m.WorkbenchPage })));
const EndpointsPage = lazy(() => import("../features/endpoints/EndpointsPage").then((m) => ({ default: m.EndpointsPage })));
const GeoIPPage = lazy(() => import("../features/geoip/GeoIPPage").then((m) => ({ default: m.GeoIPPage })));
const NodesPage = lazy(() => import("../features/nodes/NodesPage").then((m) => ({ default: m.NodesPage })));
const PlatformDetailPage = lazy(() => import("../features/platforms/PlatformDetailPage").then((m) => ({ default: m.PlatformDetailPage })));
const PlatformPage = lazy(() => import("../features/platforms/PlatformPage").then((m) => ({ default: m.PlatformPage })));
const RequestLogsPage = lazy(() => import("../features/requestLogs/RequestLogsPage").then((m) => ({ default: m.RequestLogsPage })));
const RulesPage = lazy(() => import("../features/rules/RulesPage").then((m) => ({ default: m.RulesPage })));
const SubscriptionPage = lazy(() => import("../features/subscriptions/SubscriptionPage").then((m) => ({ default: m.SubscriptionPage })));
const SystemConfigPage = lazy(() => import("../features/systemConfig/SystemConfigPage").then((m) => ({ default: m.SystemConfigPage })));
const JobsPage = lazy(() => import("../features/jobs/JobsPage").then((m) => ({ default: m.JobsPage })));
const AuditLogsPage = lazy(() => import("../features/audit/AuditLogsPage").then((m) => ({ default: m.AuditLogsPage })));

const adminRouteObjects: RouteObject[] = [
  {
    element: (
      <RequireAdmin>
        <Outlet />
      </RequireAdmin>
    ),
    children: [
      { path: "/dashboard", element: <Page><WorkbenchPage /></Page> },
      { path: "/platforms", element: <Page><PlatformPage /></Page> },
      { path: "/platforms/:platformId", element: <Page><PlatformDetailPage /></Page> },
      { path: "/subscriptions", element: <Page><SubscriptionPage /></Page> },
      { path: "/nodes", element: <Page><NodesPage /></Page> },
      { path: "/quality", element: <LegacyQualityRedirect /> },
      { path: "/endpoints", element: <Page><EndpointsPage /></Page> },
      { path: "/rules", element: <Page><RulesPage /></Page> },
      { path: "/request-logs", element: <Page><RequestLogsPage /></Page> },
      { path: "/resources", element: <Page><GeoIPPage /></Page> },
      { path: "/system-config", element: <Page><SystemConfigPage /></Page> },
      { path: "/jobs", element: <Page><JobsPage /></Page> },
      { path: "/audit", element: <Page><AuditLogsPage /></Page> },
      { path: "/admin/users", element: <Page><AdminUsersPage /></Page> },
      { path: "/admin/users/:userId", element: <Page><AdminUserDetailPage /></Page> },
      { path: "/admin/plans", element: <Page><AdminPlansPage /></Page> },
      { path: "/admin/invites", element: <Page><AdminInvitesPage /></Page> },
      { path: "/admin/redeem-codes", element: <Page><AdminRedeemCodesPage /></Page> },
      { path: "/admin/usage", element: <Page><AdminUsagePage /></Page> },
      { path: "/admin/settings", element: <Page><AdminSettingsPage /></Page> },
    ],
  },
];

/**
 * Renders the management routes, or the ordinary not-found page when the
 * current path is not one of them — the same answer the server gives for a
 * management path a tenant may not use.
 */
export function AdminRoutes() {
  return useRoutes(adminRouteObjects) ?? <NotFoundPage />;
}
