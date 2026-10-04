import { apiRequest } from "../../lib/api-client";

export type SystemInfo = {
  version: string;
  git_commit?: string;
  build_time?: string;
  build_tags?: string[];
  started_at?: string;
  panel_egress_region: string;
  panel_egress_ip: string;
};

export const SYSTEM_INFO_QUERY_KEY = ["system-info"] as const;

export function getSystemInfo(): Promise<SystemInfo> {
  return apiRequest<SystemInfo>("/api/v1/system/info");
}
