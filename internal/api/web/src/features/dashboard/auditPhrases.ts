/** The write methods the audit trail can record. */
export type AlertMethod = "POST" | "PATCH" | "PUT" | "DELETE";

/**
 * The 告警 feed reads the audit log, so its sentences come from the write routes the
 * API actually records — this is the audit trail, not a synthetic alert stream.
 *
 * The middleware stores `METHOD <route pattern>` verbatim (`auditAction` in
 * `internal/api/audit.go`, e.g. `PATCH /api/v1/platforms/{id}`), so every key below is
 * that pattern's own text, braces and all: the table matches the router literally and
 * cannot drift from it. Each pattern maps the methods it accepts to one whole phrase
 * key, because a composed "verb + noun" would have to borrow the noun's plural, unit
 * and article from the dictionary, and those keys already carry the meaning the page
 * that owns them needs.
 */
const ALERT_ACTIONS: Record<string, Partial<Record<AlertMethod, string>>> = {
  // Subscriptions: the refresh and the circuit-open cleanup, then the record itself.
  "/subscriptions/{id}/actions/refresh": { POST: "刷新订阅" },
  "/subscriptions/{id}/actions/cleanup-circuit-open-nodes": { POST: "清理订阅熔断节点" },
  "/subscriptions/{id}": { PATCH: "更新订阅", DELETE: "删除订阅" },
  "/subscriptions": { POST: "新建订阅" },

  // Nodes: the four single-node probes, and the probe that belongs to a shared IP.
  "/nodes/{hash}/actions/probe-egress": { POST: "探测节点出口" },
  "/nodes/{hash}/actions/probe-latency": { POST: "探测节点延迟" },
  "/nodes/{hash}/actions/probe-quality": { POST: "探测节点质量" },
  "/nodes/{hash}/actions/review-ippure": { POST: "复核节点纯净度" },
  "/quality/ip/{ip}/actions/probe": { POST: "探测 IP 质量" },

  // Platforms: the previews, the derived view, the reset and the leases.
  "/platforms/preview-filter": { POST: "预览平台筛选" },
  "/platforms/preview-scope": { POST: "预览平台范围" },
  "/platforms/{id}/actions/reset-to-default": { POST: "重置平台" },
  "/platforms/{id}/actions/rebuild-routable-view": { POST: "重建平台路由" },
  "/platforms/{id}/leases/{account}/actions/rotate": { POST: "轮换平台租约" },
  "/platforms/{id}/leases/{account}": { DELETE: "删除平台租约" },
  "/platforms/{id}/leases": { DELETE: "清空平台租约" },
  "/platforms/{id}": { PATCH: "更新平台", DELETE: "删除平台" },
  "/platforms": { POST: "新建平台" },

  // Intel: the jobs, the providers behind them and the checks they run.
  "/intel/jobs/{id}/actions/cancel": { POST: "取消情报任务" },
  "/intel/jobs/{id}/actions/retry-failed": { POST: "重试情报任务" },
  "/intel/jobs": { POST: "新建情报任务" },
  "/intel/providers/{id}/actions/resume": { POST: "恢复情报来源" },
  "/intel/providers/{id}/actions/refresh": { POST: "刷新情报来源" },
  "/intel/providers/{id}": { PATCH: "更新情报来源" },
  "/intel/checks/{id}": { PATCH: "更新检测项" },

  // The GeoIP dataset.
  "/geoip/actions/update-now": { POST: "更新 GeoIP 数据" },
  "/geoip/lookup": { POST: "查询 GeoIP" },

  // The console's own settings and the account-header rules.
  "/system/config": { PATCH: "更新系统配置" },
  "/account-header-rules:resolve": { POST: "解析规则" },
  "/account-header-rules/{prefix...}": { PUT: "更新规则", DELETE: "删除规则" },

  // Endpoints last: their patterns share no prefix with anything above.
  "/endpoints/{id}": { PATCH: "更新接入点", DELETE: "删除接入点" },
  "/endpoints": { POST: "新建接入点" },
};

/**
 * Splits an audit entry's `METHOD /api/v1/path` into the method chip and the phrase an
 * operator reads. `phrase` is itself a translation key. A route the table does not know
 * falls back to the route text with the API prefix stripped, so an unlisted endpoint
 * still reads as itself rather than as a guessed verb.
 */
export function alertPhrase(action: string): { method: AlertMethod | ""; phrase: string } {
  const raw = action.trim();
  if (!raw) {
    return { method: "", phrase: "" };
  }
  const separator = raw.indexOf(" ");
  const method = (separator < 0 ? raw : raw.slice(0, separator)) as AlertMethod | "";
  const route = (separator < 0 ? "" : raw.slice(separator + 1).trim()).replace(/^\/api\/v1/, "");
  const known = method === "" ? undefined : ALERT_ACTIONS[route]?.[method];
  return { method, phrase: known ?? (route || raw) };
}
