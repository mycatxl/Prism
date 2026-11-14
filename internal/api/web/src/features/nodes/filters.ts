import type { NodeListFilters } from "./types";

const booleanValue = (value: string | null) => value === "true" ? true : value === "false" ? false : undefined;

export function nodeHealthFilters(params: URLSearchParams): Pick<NodeListFilters, "enabled" | "has_outbound" | "circuit_open"> {
  const status = params.get("status");
  if (!status) return { enabled: booleanValue(params.get("enabled")), has_outbound: booleanValue(params.get("has_outbound")), circuit_open: booleanValue(params.get("circuit_open")) };
  return {
    enabled: status === "disabled" ? false : status !== "all" ? true : undefined,
    has_outbound: status === "error" ? false : status === "healthy" || status === "circuit_open" ? true : undefined,
    circuit_open: status === "healthy" ? false : status === "circuit_open" ? true : undefined,
  };
}

export function nodeHealthMode(params: URLSearchParams): string {
  if (params.get("status")) return params.get("status")!;
  const current = JSON.stringify(nodeHealthFilters(params));
  for (const status of ["all", "healthy", "circuit_open", "error", "disabled"]) {
    if (JSON.stringify(nodeHealthFilters(new URLSearchParams({ status }))) === current) return status;
  }
  return "custom";
}

export function updateNodeFilterParams(params: URLSearchParams, key: string, value: string): URLSearchParams {
  const next = new URLSearchParams(params);
  if (value || key === "capability") next.set(key, value); else next.delete(key);
  if (key === "platform_id") next.delete("capability");
  if (key === "tag_keyword") next.delete("tag");
  if (key === "status") for (const alias of ["enabled", "has_outbound", "circuit_open"]) next.delete(alias);
  if (["enabled", "has_outbound", "circuit_open"].includes(key)) next.delete("status");
  if (key === "has_egress_ip" && value === "false") next.delete("egress_ip");
  if (key === "egress_ip" && value && next.get("has_egress_ip") === "false") next.delete("has_egress_ip");
  if (key === "purity_state" && value !== "unrated") next.delete("purity_reason");
  if (key !== "page" && key !== "selected") { next.delete("page"); next.delete("cursor"); }
  return next;
}

export function nodeLatencyPreset(params: URLSearchParams): string {
  const min = params.get("latency_min") || "";
  const max = params.get("latency_max") || "";
  if (!min && !max) return "all";
  if (!min && max === "150") return "excellent";
  if (!min && max === "300") return "usable";
  if (min === "300" && !max) return "slow";
  return "custom";
}

export function updateNodeLatencyPreset(params: URLSearchParams, preset: string): URLSearchParams {
  const next = updateNodeFilterParams(params, "latency_min", preset === "slow" ? "300" : "");
  return updateNodeFilterParams(next, "latency_max", preset === "excellent" ? "150" : preset === "usable" ? "300" : "");
}

export function nodeFilterError(filter: Record<string, string>): string {
  if (filter.status && !["all", "healthy", "circuit_open", "error", "disabled"].includes(filter.status)) return "健康状态筛选无效。";
  for (const key of ["purity_min", "purity_max"]) {
    const value = filter[key];
    if (value && (!Number.isInteger(Number(value)) || Number(value) < 0 || Number(value) > 100)) return "纯净度必须是 0–100 的整数。";
  }
  if (filter.purity_min && filter.purity_max && Number(filter.purity_min) > Number(filter.purity_max)) return "纯净度下限不能大于上限。";
  for (const key of ["latency_min", "latency_max"]) {
    const value = filter[key];
    if (value && (!/^(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i.test(value.trim()) || !Number.isFinite(Number(value)) || Number(value) < 0 || Number(value) > 3_600_000)) return "延迟必须是 0–3600000 毫秒的有效数值。";
  }
  if (filter.latency_min && filter.latency_max && Number(filter.latency_min) >= Number(filter.latency_max)) return "延迟下限必须小于上限。";
  for (const key of ["has_egress_ip", "enabled", "has_outbound", "circuit_open", "native"]) {
    if (filter[key] && !["true", "false"].includes(filter[key])) return "状态筛选必须为是或否。";
  }
  for (const key of ["region", "country"]) {
    if (filter[key] && !/^[a-z]{2}$/i.test(filter[key].trim())) return "请选择两位国家或地区代码。";
  }
  if (filter.asn && (!Number.isInteger(Number(filter.asn)) || Number(filter.asn) < 1 || Number(filter.asn) > 4294967295)) return "ASN 必须是 1–4294967295 的整数。";
  if (filter.probed_since && !Number.isFinite(new Date(filter.probed_since).getTime())) return "请选择有效的探测时间。";
  return "";
}
