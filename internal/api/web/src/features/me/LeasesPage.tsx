import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, RefreshCw, Unlink } from "lucide-react";
import { useState } from "react";
import {
  ConfirmDrawer,
  ControlButton,
  DetailFact,
  EmptyWorkState,
  InspectorDrawer,
  ObjectList,
  ObjectRow,
  PageFrame,
  QueryBadge,
  StatusMark,
  WorkSurface,
} from "../../components/control-plane";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatGoDuration } from "../../lib/time";
import { useNow } from "../../lib/clock";
import { getLeases, releaseLease } from "./api";
import type { SelfLease } from "./types";

const LEASE_WARN_MS = 48 * 3_600_000;

function formatTimestamp(value: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date);
}

function formatRemainingDuration(remainingMs: number): string {
  const totalSeconds = Math.max(0, Math.floor(remainingMs / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  const parts: string[] = [];
  if (hours > 0) parts.push(`${hours}h`);
  if (minutes > 0) parts.push(`${minutes}m`);
  if (hours === 0 && seconds > 0) parts.push(`${seconds}s`);
  return formatGoDuration(parts.join("") || "0s");
}

function remainingTone(remainingMs: number): "signal" | "warn" | "alert" {
  if (remainingMs <= 0) return "alert";
  if (remainingMs <= LEASE_WARN_MS) return "warn";
  return "signal";
}

function RemainingState({ lease }: { lease: SelfLease }) {
  const { t } = useI18n();
  const now = useNow();
  const expiryMs = Date.parse(lease.expiry || "");
  if (!Number.isFinite(expiryMs)) return <StatusMark tone="neutral" label={t("未知")} />;
  const remainingMs = expiryMs - now;
  const tone = remainingTone(remainingMs);
  return <StatusMark tone={tone} label={remainingMs <= 0 ? t("已过期") : formatRemainingDuration(remainingMs)} />;
}

export function LeasesPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [selected, setSelected] = useState<SelfLease | null>(null);
  const [releaseTarget, setReleaseTarget] = useState<SelfLease | null>(null);

  const leasesQuery = useQuery({
    queryKey: ["me-leases"],
    queryFn: getLeases,
    refetchInterval: 60_000,
  });
  const releaseMutation = useMutation({
    mutationFn: (lease: SelfLease) => releaseLease(lease.platform_id, lease.account),
    onSuccess: () => {
      showToast("success", t("租约已释放"));
      setReleaseTarget(null);
      setSelected(null);
      void queryClient.invalidateQueries({ queryKey: ["me-leases"] });
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const leases = leasesQuery.data?.items ?? [];
  const now = useNow();
  const refresh = () => void leasesQuery.refetch();

  return (
    <PageFrame
      eyebrow="Self Service / leases"
      title={t("我的租约")}
      description={t("查看当前粘定的出口，并在到期前主动释放。")}
      meta={<StatusMark tone={leasesQuery.isError && !leasesQuery.data ? "warn" : leasesQuery.data ? "live" : "neutral"} label={leasesQuery.isError && !leasesQuery.data ? t("读取失败") : leasesQuery.data ? t("数据已同步") : t("等待数据")} />}
      actions={<ControlButton variant="secondary" size="sm" onPress={refresh} loading={leasesQuery.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton>}
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      <WorkSurface
        eyebrow="Lease registry"
        title={t("当前租约")}
        description={t("同一账号在租约有效期内保持同一个出口 IP。选择一行查看完整绑定关系。")}
        meta={leasesQuery.data ? t("共 {{count}} 条", { count: leases.length }) : t("未加载")}
      >
        {leasesQuery.isError && !leasesQuery.data ? (
          <EmptyWorkState title={t("租约读取失败")} detail={formatApiErrorMessage(leasesQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void leasesQuery.refetch()}>{t("重试")}</ControlButton>} />
        ) : null}
        {leasesQuery.isLoading && !leasesQuery.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载租约...")}</div> : null}
        {!leasesQuery.isLoading && !leasesQuery.isError && leases.length === 0 ? <EmptyWorkState title={t("暂无租约")} detail={t("建立代理连接后，粘定的出口会显示在这里。")} /> : null}
        {leases.length > 0 ? (
          <ObjectList>
            {leases.map((lease) => (
              <ObjectRow
                key={`${lease.platform_id}/${lease.account}`}
                active={selected?.platform_id === lease.platform_id && selected.account === lease.account}
                onClick={() => setSelected(lease)}
                leading={<span aria-hidden>↗</span>}
                title={lease.platform_name || lease.platform_id}
                subtitle={`${lease.account || t("未命名账号")} · ${lease.node_tag || t("节点标识未知")}`}
                facts={<><span>{lease.egress_ip || t("出口 IP 未知")}</span><span>{t("到期")} {formatTimestamp(lease.expiry)}</span><span>{t("最后活跃")} {formatTimestamp(lease.last_accessed)}</span></>}
                status={<RemainingState lease={lease} />}
                action={<ArrowRight size={14} aria-hidden />}
              />
            ))}
          </ObjectList>
        ) : null}
      </WorkSurface>
      <WorkSurface eyebrow="Lease policy" title={t("租约保持")} description={t("释放后下一次连接会重新选择出口。释放动作按平台和账号地址提交，不改变其他租户或平台的租约。")}>
        <div className="cp-data-grid">
          <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("当前租约")}</span><strong className="cp-data-grid__value">{leases.length}</strong></div>
          <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("自动刷新")}</span><strong className="cp-data-grid__value">60s</strong></div>
          <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("即将到期")}</span><strong className="cp-data-grid__value">{leases.filter((lease) => { const ms = Date.parse(lease.expiry) - now; return Number.isFinite(ms) && ms > 0 && ms <= LEASE_WARN_MS; }).length}</strong></div>
          <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("释放方式")}</span><strong className="cp-data-grid__value">{t("逐条")}</strong></div>
        </div>
      </WorkSurface>
      <InspectorDrawer
        open={Boolean(selected) && !releaseTarget}
        onOpenChange={(open) => { if (!open) setSelected(null); }}
        title={selected?.platform_name || selected?.platform_id || t("租约")}
        eyebrow={t("Lease inspector")}
        footer={selected ? <><ControlButton variant="ghost" size="sm" onPress={() => setSelected(null)}>{t("关闭")}</ControlButton><ControlButton variant="danger" size="sm" onPress={() => setReleaseTarget(selected)}><Unlink size={14} aria-hidden />{t("释放")}</ControlButton></> : undefined}
      >
        {selected ? (
          <div className="cp-stack">
            <RemainingState lease={selected} />
            <dl className="cp-detail-facts">
              <DetailFact label={t("平台") } value={selected.platform_name || selected.platform_id} />
              <DetailFact label={t("平台 ID")} value={selected.platform_id} mono />
              <DetailFact label={t("账号")} value={selected.account} mono />
              <DetailFact label={t("节点") } value={selected.node_tag || t("节点标识未知")} />
              <DetailFact label={t("出口 IP")} value={selected.egress_ip || t("未知")} mono />
              <DetailFact label={t("到期时间")} value={formatTimestamp(selected.expiry)} mono />
              <DetailFact label={t("最后活跃")} value={formatTimestamp(selected.last_accessed)} mono />
            </dl>
            <QueryBadge label={t("释放后下一次连接会重新选择出口。")} tone="warn" />
          </div>
        ) : null}
      </InspectorDrawer>
      <ConfirmDrawer
        open={Boolean(releaseTarget)}
        onOpenChange={(open) => { if (!open) setReleaseTarget(null); }}
        title={t("确认释放租约")}
        eyebrow={t("Destructive action")}
        message={t("确定要释放账号 {{account}} 的租约吗？释放后该账号下一次连接会重新选择出口。", { account: releaseTarget?.account ?? "" })}
        cancelLabel={t("取消")}
        confirmLabel={t("确认释放")}
        loading={releaseMutation.isPending}
        onConfirm={() => { if (releaseTarget) releaseMutation.mutate(releaseTarget); }}
      />
    </PageFrame>
  );
}
