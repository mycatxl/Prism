import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, Globe2, LoaderCircle, RefreshCw, Search, ShieldCheck } from "lucide-react";
import { useState } from "react";
import { useSearchParams } from "react-router-dom";
import {
  CommandSurface,
  ControlButton,
  ControlInput,
  ControlTable,
  ControlTableBody,
  ControlTableCell,
  ControlTableClipCell,
  ControlTableColumn,
  ControlTableHead,
  ControlTableNumericCell,
  ControlTableRow,
  ControlTableWrap,
  ControlToastContainer,
  InspectorDrawer,
  QueryBadge,
  WorkspaceCard,
} from "../../../components/control-plane";
import { EmptyState, ErrorState, LoadingState } from "../../../components/ui/QueryState";
import { Readout, ReadoutCell, ReadoutStrip } from "../../../components/ui/Readout";
import { useDebouncedValue } from "../../../hooks/useDebouncedValue";
import { useToast } from "../../../hooks/useToast";
import { useI18n } from "../../../i18n";
import { formatApiErrorMessage } from "../../../lib/error-message";
import { formatRelativeTime } from "../../../lib/time";
import { getIPQuality, getQualityStatus, inspectIP, listQuality, qualityPollingInterval } from "./api";
import { IPTypeBadge, QualityBadge, QualityDetails, VerdictBadge } from "./QualityDetails";
import { evidenceFor, inspectionErrorLabel } from "./presentation";

const pageSize = 25;

export function ExitRecordsPanel() {
  const { t } = useI18n();
  const [params, setParams] = useSearchParams();
  const selected = params.get("quality_ip") || "";
  const keyword = params.get("quality_q") || "";
  const rawPage = Number(params.get("quality_page"));
  const page = Number.isSafeInteger(rawPage) && rawPage >= 0 ? rawPage : 0;
  const deferredKeyword = useDebouncedValue(keyword);
  const [ipInput, setIPInput] = useState("");
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const status = useQuery({ queryKey: ["quality", "status"], queryFn: getQualityStatus,
    refetchInterval: query => qualityPollingInterval(query.state.data) });
  const records = useQuery({ queryKey: ["quality", "records", deferredKeyword, page],
    queryFn: () => listQuality(deferredKeyword, page), refetchInterval: qualityPollingInterval(status.data) });
  const detail = useQuery({ queryKey: ["quality", "ip", selected], queryFn: () => getIPQuality(selected),
    enabled: Boolean(selected),
    refetchInterval: query => {
      const current = query.state.data;
      if (current && (current.state === "pending" || current.state === "partial")) return 3000;
      return qualityPollingInterval(status.data);
    } });
  const inspect = useMutation({
    mutationFn: inspectIP,
    onSuccess: (result) => {
      queryClient.setQueryData(["quality", "ip", result.quality.ip], result.quality);
      void queryClient.invalidateQueries({ queryKey: ["quality"] });
      void queryClient.invalidateQueries({ queryKey: ["nodes"] });
      showToast("success", t(result.queued ? "检测已排队" : "已复用最近的检测结果"));
      setParams(previous => { const next = new URLSearchParams(previous); next.set("quality_ip", result.quality.ip || ""); return next; });
    },
    onError: error => showToast("error", formatApiErrorMessage(error, t)),
  });
  const update = (key: string, value: string) => setParams(previous => {
    const next = new URLSearchParams(previous);
    if (value) next.set(key, value); else next.delete(key);
    if (key === "quality_q") next.delete("quality_page");
    return next;
  }, { replace: true });
  const summary = detail.data ?? records.data?.items.find(item => item.ip === selected);
  const count = (value?: number) => value === undefined ? "—" : value.toLocaleString();
  const totalPages = Math.max(1, Math.ceil((records.data?.total ?? 0) / pageSize));
  const currentPage = Math.min(page, totalPages - 1);
  const jumpToPage = (raw: string) => {
    const value = Number(raw);
    if (Number.isInteger(value) && value > 0)
      update("quality_page", String(Math.max(0, Math.min(totalPages - 1, value - 1))));
  };
  return <section className="space-y-3">
    <ControlToastContainer toasts={toasts} onDismiss={dismissToast} />

    <ReadoutStrip>
      {[
        { label: "已查询 IP", value: status.data?.known_ips },
        { label: "网络证据", value: status.data?.checked_ips },
        { label: "有效纯净度评分", value: status.data?.manual_sources?.find(source => source.id === "ippure")?.current_ips },
        { label: "证据已过期", value: status.data?.stale_ips },
      ].map(item => (
        <ReadoutCell key={item.label}>
          <Readout label={t(item.label)} value={count(item.value)} />
        </ReadoutCell>
      ))}
    </ReadoutStrip>

    <WorkspaceCard
      className="min-w-0"
      title={t("IP 检测记录")}
      description={t("相同出口共享结果，节点连通状态独立记录。")}
      actions={
        <ControlButton variant="ghost" size="sm" onPress={() => void records.refetch()} isDisabled={records.isFetching}>
          <RefreshCw size={14} className={records.isFetching ? "animate-spin" : undefined} />
          {t("刷新")}
        </ControlButton>
      }
    >
      {status.isError || status.data?.enabled === false || status.data?.storage_error ? (
        <div className="mb-3 space-y-2">
          {status.isError && <ErrorState message={t("数据暂时不可用")} onRetry={() => void status.refetch()} />}
          {status.data?.enabled === false && (
            <p className="border border-warn/35 bg-warn-wash px-3 py-2 text-sm text-warn" role="status">
              {t("质量检测已停用，历史证据仍可查看。")}
            </p>
          )}
          {status.data?.storage_error && <ErrorState message={t(inspectionErrorLabel(status.data.storage_error))} />}
        </div>
      ) : null}

      <CommandSurface className="mb-3">
        <form
          className="flex min-w-0 flex-wrap items-center gap-2"
          onSubmit={event => { event.preventDefault(); inspect.mutate(ipInput.trim()); }}
        >
          <Globe2 size={16} aria-hidden className="shrink-0 text-ink-faint" />
          <ControlInput
            className="w-full sm:w-56"
            value={ipInput}
            maxLength={80}
            onChange={event => setIPInput(event.target.value)}
            placeholder={t("输入公网 IPv4 或 IPv6")}
            aria-label={t("检测 IP 地址")}
          />
          <ControlButton size="sm" type="submit" isDisabled={!ipInput.trim() || inspect.isPending || status.data?.enabled === false}>
            {inspect.isPending ? <LoaderCircle size={15} className="animate-spin" /> : <ShieldCheck size={15} />}
            {t("查询网络特征")}
          </ControlButton>
        </form>

        <div className="relative ml-auto w-full sm:w-64">
          <Search size={14} aria-hidden className="pointer-events-none absolute top-1/2 left-2.5 z-10 -translate-y-1/2 text-ink-faint" />
          <ControlInput
            className="w-full"
            value={keyword}
            onChange={event => update("quality_q", event.target.value)}
            aria-label={t("搜索质量记录")}
            placeholder={t("搜索 IP、ASN 或网络组织")}
          />
        </div>
      </CommandSurface>

      {records.isLoading && <LoadingState />}
      {records.isError && <ErrorState message={t("数据暂时不可用")} onRetry={() => void records.refetch()} />}

      {!records.isLoading && !records.isError && !records.data?.items.length && (
        <EmptyState
          title={t("为出口建立第一份质量记录")}
          hint={t("在这里输入 IP，或到节点池选择“检测质量”。新发现的出口也会自动排队。")}
        />
      )}

      {Boolean(records.data?.items.length) && (
        <ControlTableWrap aria-busy={records.isFetching}>
          <ControlTable className="min-w-[900px]">
            <ControlTableHead>
              <ControlTableColumn isRowHeader>{t("出口 IP")}</ControlTableColumn>
              <ControlTableColumn>{t("网络组织")}</ControlTableColumn>
              <ControlTableColumn>{t("IP 类型")}</ControlTableColumn>
              <ControlTableColumn>{t("纯净度参考")}</ControlTableColumn>
              <ControlTableColumn>{t("综合判定")}</ControlTableColumn>
              <ControlTableColumn className="text-right">{t("复核时间")}</ControlTableColumn>
            </ControlTableHead>
            <ControlTableBody>
              {records.data?.items.map(item => (
                <ControlTableRow key={item.ip} selected={selected === item.ip}>
                  <ControlTableClipCell title={item.ip}>
                    <ControlButton
                      type="button"
                      variant="ghost"
                      className="h-auto w-full min-w-0 justify-start gap-2 p-0 text-left"
                      onPress={() => update("quality_ip", item.ip || "")}
                    >
                      <Globe2 size={15} aria-hidden className="shrink-0 text-ink-faint" />
                      <span className="readout truncate font-medium text-ink">{item.ip}</span>
                    </ControlButton>
                  </ControlTableClipCell>
                  <ControlTableClipCell className="text-xs text-ink-faint" title={item.evidence?.organization || undefined}>
                    {item.evidence?.organization || t("等待来源数据")}
                  </ControlTableClipCell>
                  <ControlTableCell className="whitespace-nowrap"><IPTypeBadge summary={item} /></ControlTableCell>
                  <ControlTableCell className="whitespace-nowrap"><QualityBadge summary={item} /></ControlTableCell>
                  <ControlTableCell className="whitespace-nowrap"><VerdictBadge summary={item} /></ControlTableCell>
                  <ControlTableNumericCell className="text-ink-faint">
                    {formatRelativeTime(evidenceFor(item, "ippure")?.observed_at)}
                  </ControlTableNumericCell>
                </ControlTableRow>
              ))}
            </ControlTableBody>
          </ControlTable>
        </ControlTableWrap>
      )}

      {records.data && (
        <div className="mt-3 flex flex-wrap items-center justify-between gap-3 border-t border-rule-faint pt-3">
          <p className="readout text-xs text-ink-soft">
            {t("第 {{page}} / {{pages}} 页 · 显示 {{start}}-{{end}} / {{total}}", {
              page: currentPage + 1,
              pages: totalPages,
              start: records.data.total ? currentPage * pageSize + 1 : 0,
              end: Math.min((currentPage + 1) * pageSize, records.data.total),
              total: records.data.total,
            })}
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <QueryBadge label={`${t("每页")} ${pageSize}`} />
            <label className="flex items-center gap-1.5 text-xs text-ink-soft">
              <span>{t("跳至")}</span>
              <ControlInput
                key={currentPage}
                className="w-16"
                type="number"
                inputMode="numeric"
                min={1}
                max={totalPages}
                defaultValue={currentPage + 1}
                aria-label={t("选择页码")}
                disabled={records.isFetching}
                onKeyDown={(event) => {
                  if (event.key === "Enter") jumpToPage(event.currentTarget.value);
                }}
                onBlur={(event) => jumpToPage(event.currentTarget.value)}
              />
            </label>
            <ControlButton
              variant="ghost"
              size="icon"
              aria-label={t("上一页")}
              isDisabled={records.isFetching || currentPage === 0}
              onPress={() => update("quality_page", String(currentPage - 1))}
            >
              <ChevronLeft />
            </ControlButton>
            <ControlButton
              variant="ghost"
              size="icon"
              aria-label={t("下一页")}
              isDisabled={records.isFetching || currentPage >= totalPages - 1}
              onPress={() => update("quality_page", String(currentPage + 1))}
            >
              <ChevronRight />
            </ControlButton>
          </div>
        </div>
      )}
    </WorkspaceCard>

    {selected && (
      <InspectorDrawer
        open
        onOpenChange={(open) => {
          if (!open) update("quality_ip", "");
        }}
        eyebrow={<span className="readout">{selected}</span>}
        title={t("IP 质量详情")}
      >
        <div className="space-y-4">
          <p className="max-w-[68ch] text-xs text-ink-faint">{t("独立出口的质量证据")}</p>
          {detail.isLoading && !summary && <LoadingState />}
          {detail.isError && (
            <ErrorState
              message={t("数据暂时不可用")}
              onRetry={() => void detail.refetch()}
            />
          )}
          <QualityDetails
            summary={summary}
            onInspect={() => inspect.mutate(selected)}
            pending={inspect.isPending}
            disabled={status.data?.enabled === false}
          />
        </div>
      </InspectorDrawer>
    )}
  </section>;
}
