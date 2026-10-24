import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDownToLine, RefreshCw, Search } from "lucide-react";
import { useMemo, useState } from "react";
import {
  ControlButton,
  ControlInput,
  EmptyWorkState,
  DetailFact,
  StatusMark,
  WorkSurface,
  PageFrame,
  QueryBadge,
} from "../../components/control-plane";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatDateTime } from "../../lib/time";
import { getRegionName } from "../nodes/regions";
import { getGeoIPStatus, lookupIP, updateGeoIPNow } from "./api";
import type { GeoIPLookupResult } from "./types";

export function GeoIPPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [singleIP, setSingleIP] = useState("");
  const [singleResult, setSingleResult] = useState<GeoIPLookupResult | null>(null);
  const { toasts, showToast, dismissToast } = useToast();

  const statusQuery = useQuery({ queryKey: ["geoip-status"], queryFn: getGeoIPStatus, refetchInterval: 60_000 });
  const lookupMutation = useMutation({
    mutationFn: async () => {
      const ip = singleIP.trim();
      if (!ip) throw new Error(t("请输入 IP 地址"));
      return lookupIP(ip);
    },
    onSuccess: setSingleResult,
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const updateMutation = useMutation({
    mutationFn: updateGeoIPNow,
    onSuccess: async (result) => {
      await queryClient.invalidateQueries({ queryKey: ["geoip-status"] });
      if (result.status === "ok") {
        showToast("success", t("GeoIP 数据库更新任务已执行"));
      } else {
        showToast("error", t("GeoIP 数据库更新未完成（状态：{{status}}）", { status: result.status || "-" }));
      }
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const status = statusQuery.data;
  const hasDBTime = Boolean(status?.db_mtime);
  const hasNextSchedule = Boolean(status?.next_scheduled_update);
  const singleRegion = useMemo(() => {
    if (!singleResult?.region) return t("（空）");
    const code = singleResult.region.toUpperCase();
    const name = getRegionName(code);
    return name ? `${code} ${name}` : code;
  }, [singleResult, t]);

  return (
    <PageFrame
      eyebrow="Governance / resources"
      title={t("资源")}
      description={t("检查 GeoIP 数据库的加载状态，并对单个地址执行区域查询。")}
      meta={<StatusMark tone={statusQuery.isError && !statusQuery.data ? "warn" : statusQuery.data ? "live" : "neutral"} label={statusQuery.isError && !statusQuery.data ? t("读取失败") : statusQuery.data ? t("资源状态已同步") : t("等待资源状态")} />}
      actions={<ControlButton variant="secondary" size="sm" onPress={() => void statusQuery.refetch()} loading={statusQuery.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton>}
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <div className="cp-split">
        <WorkSurface eyebrow="Governance / database" title={t("GeoIP 数据库状态")} description={t("当前加载时间与下一次计划更新时间来自 GeoIP 状态接口。")}>
          {statusQuery.isError && !statusQuery.data ? <EmptyWorkState title={t("GeoIP 状态读取失败")} detail={formatApiErrorMessage(statusQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void statusQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
          {statusQuery.isLoading && !statusQuery.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载 GeoIP 状态")}</div> : null}
          {status ? (
            <div className="cp-stack">
              <dl className="cp-detail-facts">
                <DetailFact label={t("数据库状态")} value={<StatusMark tone={hasDBTime ? "signal" : "warn"} label={hasDBTime ? t("数据库已加载") : t("数据库未加载")} />} />
                <DetailFact label={t("数据库更新时间")} value={hasDBTime ? formatDateTime(status.db_mtime) : "-"} mono />
                <DetailFact label={t("下次计划更新")} value={hasNextSchedule ? formatDateTime(status.next_scheduled_update) : "-"} mono />
              </dl>
              <div className="cp-drawer-actions">
                <ControlButton variant="secondary" size="sm" onPress={() => void updateMutation.mutateAsync()} loading={updateMutation.isPending}>
                  <ArrowDownToLine size={14} aria-hidden className={cn(updateMutation.isPending && "animate-spin")} />
                  {t("立即更新")}
                </ControlButton>
                <QueryBadge label={hasDBTime ? t("已有数据库时间") : t("等待数据库加载")} tone={hasDBTime ? "signal" : "warn"} />
              </div>
            </div>
          ) : null}
        </WorkSurface>

        <WorkSurface eyebrow="Governance / lookup" title={t("单 IP 查询")} description={t("输入 IP 后查询服务端当前 GeoIP 区域结果。")}>
          <form className="cp-form-stack" onSubmit={(event) => { event.preventDefault(); void lookupMutation.mutateAsync(); }}>
            <ControlInput label={t("IP 地址")} hint={t("例如 8.8.8.8")} className="readout" value={singleIP} onChange={(event) => setSingleIP(event.target.value)} />
            <div className="cp-drawer-actions">
              <ControlButton type="submit" variant="primary" size="sm" loading={lookupMutation.isPending}><Search size={14} aria-hidden />{t("查询")}</ControlButton>
            </div>
          </form>
          {singleResult ? (
            <dl className="cp-detail-facts" aria-live="polite">
              <DetailFact label="IP" value={singleResult.ip || "-"} mono />
              <DetailFact label={t("区域")} value={singleRegion} />
            </dl>
          ) : (
            <EmptyWorkState title={t("输入 IP 执行查询")} detail={t("查询结果会在这里显示，不会生成未返回的区域信息。")} />
          )}
        </WorkSurface>
      </div>
    </PageFrame>
  );
}

export default GeoIPPage;
